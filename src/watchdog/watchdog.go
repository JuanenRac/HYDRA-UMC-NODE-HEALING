// HYDRA-UMC-NODE-HEALING - watchdog package
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The real heartbeat/detection loop described in the README's "HEALING
// WORKFLOW" diagram: poll every registered node's HealthService.Check()
// (the shared contract in HYDRA-UMC-ORCHESTRATOR/proto/hydra_common.proto,
// vendored locally as src/healthpb), classify each result, and fire
// state-change callbacks - never per-tick spam - so a Reactor can plug in
// the actual "SOFT-REBOOT" / "FAILOVER: move jobs" actions once
// HYDRA-UMC-ORCHESTRATOR has a real API to call for that. This package
// only does detection; it deliberately does not decide what "recovery"
// means for a given node - that decision belongs to whoever implements
// Reactor.
package watchdog

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	healthpb "github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/healthpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Status is the watchdog's own classification of a node, one notch more
// detailed than healthpb.HealthState because it also has to represent
// "we could not even reach it" - something the node itself can never
// report about itself.
type Status int

const (
	// StatusUnknown is the state of a node that has never been checked yet.
	StatusUnknown Status = iota
	StatusHealthy
	StatusDegraded
	StatusUnhealthy
	// StatusUnreachable means the dial or the RPC itself failed (timeout,
	// connection refused, process not running) - distinct from
	// HEALTH_STATE_UNHEALTHY, which requires the node to be alive enough
	// to answer the RPC and say so about itself.
	StatusUnreachable
	// StatusInvalid means the node answered the RPC, but its own
	// self-reported identity cannot be trusted: either it omitted
	// NodeIdentity entirely, or it reported a name that does not match
	// the Node this watchdog dialed. A node that cannot correctly say who
	// it is must never have its claimed health state trusted either - it
	// could be a misconfigured service bound to the wrong port. Unlike
	// StatusUnreachable, this is never retried: retrying does not fix a
	// node lying about (or not knowing) its own name.
	StatusInvalid
	// StatusMisconfigured means the node's HTTP health URL answered with a
	// redirect or a 4xx (a moved route, a missing credential): the problem is
	// in the configuration, and says nothing about whether the node itself is
	// alive, so it never triggers an automatic recovery.
	StatusMisconfigured
)

func (s Status) String() string {
	switch s {
	case StatusHealthy:
		return "HEALTHY"
	case StatusDegraded:
		return "DEGRADED"
	case StatusUnhealthy:
		return "UNHEALTHY"
	case StatusUnreachable:
		return "UNREACHABLE"
	case StatusInvalid:
		return "INVALID"
	case StatusMisconfigured:
		return "MISCONFIGURED"
	default:
		return "UNKNOWN"
	}
}

// Node is one entry in the static registry this v0 watchdog polls.
//
// Why static and not discovered dynamically via HYDRA-UMC-SWARM-SYNC (the
// README's stated long-term source of truth for "every cell in the
// swarm"): SWARM-SYNC is itself andamiaje today, with no real API yet to
// query. Hardcoding a fixed list here would be worse than an honest,
// explicit config file - see cmd's config loader. Swap this for a real
// SWARM-SYNC client once that project has one.
type Node struct {
	Name    string // must match healthpb.NodeIdentity.Name the node reports
	Address string // host:port, e.g. "127.0.0.1:50100"
	// HealthURL, when set, makes the watchdog probe this node with an HTTP
	// GET instead of the gRPC HealthService (see httpprobe.go). Most
	// services in this ecosystem speak HTTP only and none of them
	// implement the gRPC HealthService, so a node left on the gRPC probe
	// is reported UNREACHABLE no matter how healthy it is.
	HealthURL string
	// DispatchNode is the name this node has in HYDRA-UMC-ORCHESTRATOR's
	// mission dispatch (the string passed to POST /missions/:id/dispatch).
	// It is a different name space from Name, which is the SERVICE being
	// watched: recovery asks the Orchestrator to requeue the missions
	// dispatched to DispatchNode, and an entry without one never asks.
	DispatchNode string
	// Headers are sent with every HTTP health probe of this node (for example an
	// Authorization header for a health route that wants a token). They come from the
	// registry file, which is the operator's, and their values are never logged.
	Headers map[string]string
}

// Reactor receives state-change events. The default used by main() only
// logs - it deliberately does not call HYDRA-UMC-ORCHESTRATOR to trigger a
// real failover, because that API does not exist yet either. This
// interface is the seam where that gets plugged in later, without
// touching the detection logic in this file again.
type Reactor interface {
	OnTransition(node Node, from, to Status, detail string)
}

// LogReactor is the default Reactor: prints every transition to a writer.
// Real enough to depend on in production (this is genuinely how an
// operator would see failures today, via journalctl/docker logs), simple
// enough not to pretend to be the failover engine it isn't yet.
type LogReactor struct {
	Printf func(format string, args ...any)
}

func (r LogReactor) OnTransition(node Node, from, to Status, detail string) {
	printf := r.Printf
	if printf == nil {
		printf = func(format string, args ...any) { fmt.Printf(format, args...) }
	}
	if detail != "" {
		printf("[node-healing] %s: %s -> %s (%s)\n", node.Name, from, to, detail)
	} else {
		printf("[node-healing] %s: %s -> %s\n", node.Name, from, to)
	}
}

// Watchdog polls a fixed set of nodes on an interval and reports state
// transitions (not every tick) to a Reactor.
type Watchdog struct {
	Nodes        []Node
	Interval     time.Duration
	CheckTimeout time.Duration
	RetryPolicy  RetryPolicy // bounded retries + backoff for transport-level failures - see retry.go
	Reactor      Reactor
	DialOptions  []grpc.DialOption // overridable for tests (in-process bufconn)
	HTTPClient   *http.Client      // used for nodes with a HealthURL; nil builds one bounded by CheckTimeout

	mu    sync.Mutex
	state map[string]Status // keyed by Node.Name
	stats map[string]*nodeStats
	// Nodes whose poll has not finished yet. A tick starts a poll only for the nodes not in it, and does not wait for the
	// others: a node that answers slowly (or not at all) neither delays the detection of the rest nor piles up polls.
	polling map[string]bool
	// Every poll started by Run, so that Run waits for them before it closes the connections.
	active sync.WaitGroup

	// One gRPC connection per node address, kept between polls: a healthy node is not dialed and torn down
	// every tick. A connection that fails is dropped, so the next attempt dials from scratch (no reconnect
	// back-off waiting in the way of seeing a node come back).
	connMu sync.Mutex
	conns  map[string]*grpc.ClientConn
}

// nodeStats is what the status endpoint reports about one node.
type nodeStats struct {
	since       time.Time
	lastCheck   time.Time
	lastLatency time.Duration
	checks      int64
	transitions int64
}

// NewWatchdog builds a Watchdog with production-sane defaults (5s poll
// interval, 2s per-check timeout, DefaultRetryPolicy() for transient
// network failures, insecure transport credentials - LAN traffic between
// trusted nodes, same threat model already documented for
// HYDRA-UMC-SERVER's CORS/mTLS posture).
func NewWatchdog(nodes []Node, reactor Reactor) *Watchdog {
	return &Watchdog{
		Nodes:        nodes,
		Interval:     5 * time.Second,
		CheckTimeout: 2 * time.Second,
		RetryPolicy:  DefaultRetryPolicy(),
		Reactor:      reactor,
		DialOptions:  []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
		state:        make(map[string]Status),
		stats:        make(map[string]*nodeStats),
		polling:      make(map[string]bool),
		conns:        make(map[string]*grpc.ClientConn),
	}
}

// SetNodes replaces the list of watched nodes while the watchdog runs (a reload of the registry). Nodes that
// are no longer listed lose their state and statistics, and the connections nobody uses any more are closed.
// A node that stays keeps its state, so a reload never announces a transition that did not happen.
func (w *Watchdog) SetNodes(nodes []Node) {
	w.mu.Lock()
	keep := make(map[string]bool, len(nodes))
	addresses := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		keep[n.Name] = true
		if n.HealthURL == "" {
			addresses[n.Address] = true
		}
	}
	for name := range w.state {
		if !keep[name] {
			delete(w.state, name)
			delete(w.stats, name)
		}
	}
	w.Nodes = append([]Node(nil), nodes...)
	w.mu.Unlock()

	w.connMu.Lock()
	for address, conn := range w.conns {
		if !addresses[address] {
			_ = conn.Close()
			delete(w.conns, address)
		}
	}
	w.connMu.Unlock()
}

func (w *Watchdog) currentNodes() []Node {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Node(nil), w.Nodes...)
}

// Close closes the connections the watchdog keeps. Run calls it when it stops.
func (w *Watchdog) Close() {
	w.connMu.Lock()
	defer w.connMu.Unlock()
	for address, conn := range w.conns {
		_ = conn.Close()
		delete(w.conns, address)
	}
}

// connection returns the kept connection to address, creating it the first time. Nothing is dialed here:
// the first call on it connects.
func (w *Watchdog) connection(address string) (*grpc.ClientConn, error) {
	w.connMu.Lock()
	defer w.connMu.Unlock()
	if w.conns == nil {
		w.conns = make(map[string]*grpc.ClientConn)
	}
	if conn, ok := w.conns[address]; ok {
		return conn, nil
	}
	// passthrough: the address is handed to the dialer as it is, as grpc.DialContext always did
	conn, err := grpc.NewClient("passthrough:///"+address, w.DialOptions...)
	if err != nil {
		return nil, err
	}
	w.conns[address] = conn
	return conn, nil
}

// dropConnection closes conn and forgets it, if it is still the one kept for address.
func (w *Watchdog) dropConnection(address string, conn *grpc.ClientConn) {
	w.connMu.Lock()
	if w.conns[address] == conn {
		delete(w.conns, address)
	}
	w.connMu.Unlock()
	_ = conn.Close()
}

// Run polls every node once per Interval until ctx is cancelled. Blocking;
// call it in its own goroutine (or as main()'s last call, which is what
// this repo's main.go does).
func (w *Watchdog) Run(ctx context.Context) {
	// deferred in this order so that the polls in flight finish (they stop with ctx) before the connections are closed
	defer w.Close()
	defer w.active.Wait()
	w.startDue(ctx)
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.startDue(ctx)
		}
	}
}

// startDue starts a poll of every node whose previous poll has finished and returns a group that is done when those polls are.
// Run does not wait on it: the ticks of the nodes that answer keep their rhythm whatever a node that does not answer takes
// (up to RetryPolicy.MaxAttempts times CheckTimeout plus the back-off, which can be longer than Interval). A node still being polled
// at the next tick is skipped, not queued.
func (w *Watchdog) startDue(ctx context.Context) *sync.WaitGroup {
	group := &sync.WaitGroup{}
	for _, n := range w.currentNodes() {
		w.mu.Lock()
		if w.polling == nil {
			w.polling = make(map[string]bool)
		}
		busy := w.polling[n.Name]
		if !busy {
			w.polling[n.Name] = true
		}
		w.mu.Unlock()
		if busy {
			continue
		}
		group.Add(1)
		w.active.Add(1)
		go func(n Node) {
			defer func() {
				w.mu.Lock()
				delete(w.polling, n.Name)
				w.mu.Unlock()
				w.active.Done()
				group.Done()
			}()
			w.pollOne(ctx, n)
		}(n)
	}
	return group
}

// pollAll polls every node once and waits for all of them (what the tests and a one-off check want; Run does not wait).
func (w *Watchdog) pollAll(ctx context.Context) {
	w.startDue(ctx).Wait()
}

func (w *Watchdog) pollOne(ctx context.Context, n Node) {
	started := time.Now()
	status, detail := w.checkNode(ctx, n)
	latency := time.Since(started)
	w.mu.Lock()
	prev, seen := w.state[n.Name]
	w.state[n.Name] = status
	stats := w.stats[n.Name]
	if stats == nil {
		stats = &nodeStats{}
		w.stats[n.Name] = stats
	}
	stats.checks++
	stats.lastCheck = started
	stats.lastLatency = latency
	if !seen || prev != status {
		stats.transitions++
		stats.since = started
	}
	w.mu.Unlock()

	// Only fire the reactor on an actual state change - the first
	// observation always counts as one (from StatusUnknown), everything
	// after that only if the classification differs from last tick. This
	// is what keeps a healthy swarm silent instead of logging "OK" every
	// 5 seconds forever.
	if !seen || prev != status {
		if w.Reactor != nil {
			w.Reactor.OnTransition(n, prev, status, detail)
		}
	}
}

// checkNode runs attemptCheck up to RetryPolicy.MaxAttempts times,
// waiting RetryPolicy.Backoff() between attempts, and returns as soon as
// an attempt is non-retryable (a real classification, or an invalid
// identity - see StatusInvalid). Exported indirectly via CheckOnce() for
// tests and a future CLI subcommand that want a single check without the
// polling loop.
func (w *Watchdog) checkNode(ctx context.Context, n Node) (Status, string) {
	policy := w.RetryPolicy
	if err := policy.Validate(); err != nil {
		// A misconfigured policy must never silently disable retries or
		// panic mid-poll - fall back to a policy known to be sane.
		policy = DefaultRetryPolicy()
	}

	var status Status
	var detail string
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		var retryable bool
		status, detail, retryable = w.attemptCheck(ctx, n)
		if !retryable {
			return status, detail
		}
		if attempt < policy.MaxAttempts {
			timer := time.NewTimer(policy.Backoff(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return status, detail
			case <-timer.C:
			}
		}
	}
	return status, detail
}

// attemptCheck performs one real gRPC HealthService.Check() call and
// classifies the outcome. retryable is true only for transport-level
// failures (dial/RPC error) - never for a node that answered but cannot
// be trusted (StatusInvalid), since no amount of retrying fixes that.
func (w *Watchdog) attemptCheck(ctx context.Context, n Node) (status Status, detail string, retryable bool) {
	if n.HealthURL != "" {
		return w.attemptHTTPCheck(ctx, n)
	}
	conn, err := w.connection(n.Address)
	if err != nil {
		return StatusUnreachable, err.Error(), true
	}

	client := healthpb.NewHealthServiceClient(conn)
	checkCtx, cancel := context.WithTimeout(ctx, w.CheckTimeout)
	defer cancel()
	report, err := client.Check(checkCtx, &healthpb.Empty{})
	if err != nil {
		// a failed call may be a node that went away and a connection that went with it: the next attempt dials again
		w.dropConnection(n.Address, conn)
		return StatusUnreachable, err.Error(), true
	}

	identity := report.GetIdentity()
	if identity == nil || identity.GetName() == "" {
		return StatusInvalid, fmt.Sprintf("node at %s answered but reported no self-identity - refusing to trust its health state", n.Address), false
	}
	if identity.GetName() != n.Name {
		return StatusInvalid, fmt.Sprintf("node at %s self-identified as %q, expected %q - refusing to trust its health state", n.Address, identity.GetName(), n.Name), false
	}

	switch report.GetState() {
	case healthpb.HealthState_HEALTH_STATE_OK:
		return StatusHealthy, report.GetDetail(), false
	case healthpb.HealthState_HEALTH_STATE_DEGRADED:
		return StatusDegraded, report.GetDetail(), false
	case healthpb.HealthState_HEALTH_STATE_UNHEALTHY:
		return StatusUnhealthy, report.GetDetail(), false
	default:
		// A node that answers but reports HEALTH_STATE_UNSPECIFIED is
		// itself a real signal (it's alive, but its own health logic
		// hasn't classified itself) - treat it as degraded rather than
		// silently mapping it to healthy.
		return StatusDegraded, "node reported HEALTH_STATE_UNSPECIFIED", false
	}
}

// CheckOnce runs a single check outside the polling loop - used by a
// future CLI subcommand ("node-healing check <name>") and by tests.
func (w *Watchdog) CheckOnce(ctx context.Context, n Node) (Status, string) {
	return w.checkNode(ctx, n)
}
