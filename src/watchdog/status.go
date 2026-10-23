// HYDRA-UMC-NODE-HEALING - watchdog status endpoint
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The watchdog watches the others; this is how the others (STUDIO, the OPS agent, a Prometheus) watch
// the watchdog. It is a read-only HTTP endpoint, off unless -listen is given:
//
//	GET /health   always 200 while the process runs: version, uptime, how many nodes are watched.
//	              No node names, no token: it is what a liveness probe asks.
//	GET /metrics  Prometheus text: the status of every node, when it was last checked and how long the
//	              check took, how many checks and transitions, and the recovery requests sent.
//	              Wants "Authorization: Bearer <token>" when a token is set.
//
// It binds loopback unless a token is set: node names and states are not for everybody on the network.
package watchdog

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// NodeSnapshot is what is known about one node at one moment.
type NodeSnapshot struct {
	Name        string
	Status      Status
	Since       time.Time
	LastCheck   time.Time
	LastLatency time.Duration
	Checks      int64
	Transitions int64
}

// Snapshot is the state of every watched node, sorted by name. A node not checked yet has StatusUnknown.
func (w *Watchdog) Snapshot() []NodeSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]NodeSnapshot, 0, len(w.Nodes))
	for _, n := range w.Nodes {
		snap := NodeSnapshot{Name: n.Name, Status: w.state[n.Name]}
		if s := w.stats[n.Name]; s != nil {
			snap.Since, snap.LastCheck, snap.LastLatency, snap.Checks, snap.Transitions = s.since, s.lastCheck, s.lastLatency, s.checks, s.transitions
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RecoveryCounter is implemented by a Reactor that sends recovery requests.
type RecoveryCounter interface {
	RecoveryRequests() int64
}

// StatusServer serves /health and /metrics.
type StatusServer struct {
	Watchdog *Watchdog
	Version  string
	Started  time.Time
	// Token, when set, is demanded on /metrics.
	Token string
	// Recoveries, when set, is read for the recovery request counter.
	Recoveries RecoveryCounter
	// Now defaults to time.Now.
	Now func() time.Time
}

func (s *StatusServer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ValidateListen refuses an address that is not loopback unless a token protects the metrics.
func ValidateListen(address, token string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", address, err)
	}
	if token != "" {
		return nil
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("listen address %q is not loopback: set -status-token (or STATUS_TOKEN) so the metrics are not open to the network", address)
}

func (s *StatusServer) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	header := r.Header.Get("Authorization")
	scheme, presented, found := strings.Cut(header, " ")
	return found && scheme == "Bearer" && subtle.ConstantTimeCompare([]byte(presented), []byte(s.Token)) == 1
}

// Handler is the endpoint as an http.Handler.
func (s *StatusServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/metrics", s.metrics)
	return mux
}

func (s *StatusServer) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"version":       s.Version,
		"uptimeSeconds": int64(s.now().Sub(s.Started).Seconds()),
		"nodes":         len(s.Watchdog.Snapshot()),
	})
}

// escapeLabel makes a node name safe inside a Prometheus label value.
func escapeLabel(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")
	return replacer.Replace(value)
}

func (s *StatusServer) metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "a valid bearer token is required", http.StatusUnauthorized)
		return
	}
	snaps := s.Watchdog.Snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP hydra_node_healing_info The watchdog itself.\n# TYPE hydra_node_healing_info gauge\nhydra_node_healing_info{version=\"%s\"} 1\n", escapeLabel(s.Version))
	fmt.Fprintf(&b, "# HELP hydra_node_healing_nodes How many nodes are watched.\n# TYPE hydra_node_healing_nodes gauge\nhydra_node_healing_nodes %d\n", len(snaps))
	b.WriteString("# HELP hydra_node_healing_node_status 1 for the current status of a node.\n# TYPE hydra_node_healing_node_status gauge\n")
	for _, snap := range snaps {
		fmt.Fprintf(&b, "hydra_node_healing_node_status{node=\"%s\",status=\"%s\"} 1\n", escapeLabel(snap.Name), snap.Status)
	}
	b.WriteString("# HELP hydra_node_healing_node_last_check_timestamp_seconds When the node was last checked.\n# TYPE hydra_node_healing_node_last_check_timestamp_seconds gauge\n")
	for _, snap := range snaps {
		if !snap.LastCheck.IsZero() {
			fmt.Fprintf(&b, "hydra_node_healing_node_last_check_timestamp_seconds{node=\"%s\"} %d\n", escapeLabel(snap.Name), snap.LastCheck.Unix())
		}
	}
	b.WriteString("# HELP hydra_node_healing_node_check_duration_seconds How long the last check took, retries included.\n# TYPE hydra_node_healing_node_check_duration_seconds gauge\n")
	for _, snap := range snaps {
		if snap.Checks > 0 {
			fmt.Fprintf(&b, "hydra_node_healing_node_check_duration_seconds{node=\"%s\"} %.3f\n", escapeLabel(snap.Name), snap.LastLatency.Seconds())
		}
	}
	b.WriteString("# HELP hydra_node_healing_node_checks_total Checks made.\n# TYPE hydra_node_healing_node_checks_total counter\n")
	for _, snap := range snaps {
		fmt.Fprintf(&b, "hydra_node_healing_node_checks_total{node=\"%s\"} %d\n", escapeLabel(snap.Name), snap.Checks)
	}
	b.WriteString("# HELP hydra_node_healing_node_transitions_total Changes of status seen.\n# TYPE hydra_node_healing_node_transitions_total counter\n")
	for _, snap := range snaps {
		fmt.Fprintf(&b, "hydra_node_healing_node_transitions_total{node=\"%s\"} %d\n", escapeLabel(snap.Name), snap.Transitions)
	}
	if s.Recoveries != nil {
		fmt.Fprintf(&b, "# HELP hydra_node_healing_recovery_requests_total Recovery requests sent to the orchestrator.\n# TYPE hydra_node_healing_recovery_requests_total counter\nhydra_node_healing_recovery_requests_total %d\n", s.Recoveries.RecoveryRequests())
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

// Serve listens on address and answers until ctx ends. The address is validated first.
func (s *StatusServer) Serve(ctx context.Context, address string) error {
	if err := ValidateListen(address, s.Token); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
