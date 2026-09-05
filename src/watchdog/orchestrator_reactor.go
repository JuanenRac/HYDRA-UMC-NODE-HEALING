// HYDRA-UMC-NODE-HEALING - watchdog package: orchestrator_reactor.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// The real Reactor watchdog.go's own comment already named as the seam
// to fill in "once HYDRA-UMC-ORCHESTRATOR has a real API to call for
// that" - it now does (HYDRA-UMC-ORCHESTRATOR's own server.rs,
// POST /nodes/:node/recover). OrchestratorReactor calls that real HTTP
// endpoint whenever a node transitions INTO StatusUnreachable or
// StatusInvalid - the two classifications that mean this node can no
// longer be trusted to keep running whatever it was assigned, not every
// transition (a Degraded node may still be doing real work; a node
// recovering back to Healthy needs no recovery action at all).
//
// Real, honest coupling this file does not try to paper over: this
// watchdog's own Node.Name (the identity a HydraNode reports over
// HealthService) and Orchestrator's own mission-dispatch node names
// (whatever a caller passed to POST /missions/:id/dispatch) are the
// SAME string space in a real deployment only if whoever writes
// nodes.json deliberately keeps them consistent - this reactor forwards
// Node.Name as-is, it does not invent a translation between the two.
package watchdog

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// defaultRecoveryTimeout bounds the recovery POST below when no Client is
// given. Found in an ecosystem-wide software-improvements audit:
// http.DefaultClient has no timeout at all, so if Orchestrator itself is
// hung - the most likely scenario during a real incident - this call could
// block forever per unhealthy-node transition, leaking a goroutine exactly
// when the system is most compromised.
const defaultRecoveryTimeout = 5 * time.Second

// OrchestratorReactor wraps LogReactor's own real logging (an operator
// must still see every transition via journalctl regardless of whether
// the HTTP call below succeeds) with a real recovery request to
// HYDRA-UMC-ORCHESTRATOR for the two classifications that mean a node's
// in-flight work needs to be requeued elsewhere.
type OrchestratorReactor struct {
	// BaseURL is Orchestrator's own HTTP API base, e.g.
	// "http://127.0.0.1:8114" - no trailing slash required.
	BaseURL string
	// Client defaults to a client bounded by defaultRecoveryTimeout, not
	// http.DefaultClient (which has no timeout) - overridable for tests.
	Client *http.Client
	// Printf defaults to fmt.Printf, same as LogReactor's own default.
	Printf func(format string, args ...any)
}

func (r OrchestratorReactor) printf(format string, args ...any) {
	printf := r.Printf
	if printf == nil {
		printf = func(format string, args ...any) { fmt.Printf(format, args...) }
	}
	printf(format, args...)
}

func (r OrchestratorReactor) OnTransition(node Node, from, to Status, detail string) {
	// Real logging first, unconditionally - the same visibility
	// LogReactor already provides must never regress just because this
	// reactor also tries something the log-only one didn't.
	LogReactor{Printf: r.Printf}.OnTransition(node, from, to, detail)

	if to != StatusUnreachable && to != StatusInvalid {
		return
	}

	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: defaultRecoveryTimeout}
	}
	url := fmt.Sprintf("%s/nodes/%s/recover", strings.TrimSuffix(r.BaseURL, "/"), node.Name)
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		r.printf("[node-healing] recovery request to orchestrator failed for %s: %v\n", node.Name, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.printf("[node-healing] orchestrator recovery for %s returned %s\n", node.Name, resp.Status)
		return
	}
	r.printf("[node-healing] requested orchestrator recovery for %s\n", node.Name)
}
