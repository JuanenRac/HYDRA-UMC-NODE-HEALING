// HYDRA-UMC-NODE-HEALING - watchdog package
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
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
	"sync"
	"time"

	healthpb "github.com/JuanenRac/hydra-umc-node-healing/src/healthpb"
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

	mu    sync.Mutex
	state map[string]Status // keyed by Node.Name
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
	}
}

// Run polls every node once per Interval until ctx is cancelled. Blocking;
// call it in its own goroutine (or as main()'s last call, which is what
// this repo's main.go does).
func (w *Watchdog) Run(ctx context.Context) {
	w.pollAll(ctx)
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollAll(ctx)
		}
	}
}

func (w *Watchdog) pollAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, n := range w.Nodes {
		wg.Add(1)
		go func(n Node) {
			defer wg.Done()
			w.pollOne(ctx, n)
		}(n)
	}
	wg.Wait()
}

func (w *Watchdog) pollOne(ctx context.Context, n Node) {
	status, detail := w.checkNode(ctx, n)
	w.mu.Lock()
	prev, seen := w.state[n.Name]
	w.state[n.Name] = status
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
	dialCtx, cancel := context.WithTimeout(ctx, w.CheckTimeout)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx, n.Address, append([]grpc.DialOption{grpc.WithBlock()}, w.DialOptions...)...)
	if err != nil {
		return StatusUnreachable, err.Error(), true
	}
	defer conn.Close()

	client := healthpb.NewHealthServiceClient(conn)
	checkCtx, cancel2 := context.WithTimeout(ctx, w.CheckTimeout)
	defer cancel2()
	report, err := client.Check(checkCtx, &healthpb.Empty{})
	if err != nil {
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
