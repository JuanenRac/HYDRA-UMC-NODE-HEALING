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
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultRecoveryTimeout bounds the recovery POST below when no Client is
// given. http.DefaultClient has no timeout at all, so if Orchestrator itself is
// hung - the most likely scenario during a real incident - this call could
// block forever per unhealthy-node transition, leaking a goroutine exactly
// when the system is most compromised.
const defaultRecoveryTimeout = 5 * time.Second

// DefaultRecoveryRetryPolicy bounds HEAL-01's own background retry (see
// startBackgroundRetry): up to 5 attempts, starting at 2s and capped at
// 30s, so a briefly-restarting Orchestrator is still reached without
// hammering it, and a genuinely long-term outage stops retrying instead
// of running forever. Reuses RetryPolicy - the same deterministic,
// directly-assertable backoff already used for per-tick node checks -
// rather than inventing a second backoff shape for the same math.
func DefaultRecoveryRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 5, BaseDelay: 2 * time.Second, MaxDelay: 30 * time.Second}
}

// OrchestratorReactor wraps LogReactor's own real logging (an operator
// must still see every transition via journalctl regardless of whether
// the HTTP call below succeeds) with a real recovery request to
// HYDRA-UMC-ORCHESTRATOR for the two classifications that mean a node's
// in-flight work needs to be requeued elsewhere.
//
// HEAL-01 (P1):
// the recovery request used to be tried exactly once, at the moment of
// the transition, with no follow-up - if Orchestrator itself happened to
// be unreachable at that exact instant and the node then just stayed
// down (no FURTHER transition, since its classification never changes
// tick to tick while it stays unreachable), nothing ever repeated the
// request once Orchestrator came back. A bounded, cancellable background
// retry (see startBackgroundRetry) now covers exactly that gap. Use
// `&OrchestratorReactor{...}` (a pointer), not a bare value - the
// pending-retry bookkeeping below needs one real, shared instance, the
// same discipline Watchdog itself already uses for its own state map.
type OrchestratorReactor struct {
	// BaseURL is Orchestrator's own HTTP API base, e.g.
	// "http://127.0.0.1:8114" - no trailing slash required.
	BaseURL string
	// Client defaults to a client bounded by defaultRecoveryTimeout, not
	// http.DefaultClient (which has no timeout) - overridable for tests.
	Client *http.Client
	// Printf defaults to fmt.Printf, same as LogReactor's own default.
	Printf func(format string, args ...any)
	// RecoveryRetryPolicy bounds the background retry below - defaults to
	// DefaultRecoveryRetryPolicy() when left zero-valued.
	RecoveryRetryPolicy RetryPolicy

	mu      sync.Mutex
	pending map[string]context.CancelFunc // node name -> cancel for its in-flight background retry, if any
}

func (r *OrchestratorReactor) printf(format string, args ...any) {
	printf := r.Printf
	if printf == nil {
		printf = func(format string, args ...any) { fmt.Printf(format, args...) }
	}
	printf(format, args...)
}

func (r *OrchestratorReactor) OnTransition(node Node, from, to Status, detail string) {
	// Real logging first, unconditionally - the same visibility
	// LogReactor already provides must never regress just because this
	// reactor also tries something the log-only one didn't.
	LogReactor{Printf: r.Printf}.OnTransition(node, from, to, detail)

	// Any earlier background retry for this exact node is no longer
	// relevant to THIS transition: either the node just recovered on its
	// own (this transition isn't even Unreachable/Invalid below - the
	// pending retry would otherwise keep nagging Orchestrator about a
	// node that is no longer down), or a fresh Unreachable/Invalid event
	// should restart the retry count from attempt 1 rather than racing
	// an older loop's own attempts. Cancelling first, unconditionally,
	// keeps exactly one retry loop per node alive at any time.
	r.cancelPendingLocked(node.Name)

	if to != StatusUnreachable && to != StatusInvalid {
		return
	}

	if r.attemptRecovery(node) {
		return
	}
	r.startBackgroundRetry(node)
}

func (r *OrchestratorReactor) cancelPendingLocked(nodeName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.pending[nodeName]; ok {
		cancel()
		delete(r.pending, nodeName)
	}
}

// attemptRecovery makes exactly one real recovery POST and logs the
// outcome - the same real behavior OnTransition always had for its own
// first attempt. Returns whether it succeeded (HTTP 200).
func (r *OrchestratorReactor) attemptRecovery(node Node) bool {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: defaultRecoveryTimeout}
	}
	url := fmt.Sprintf("%s/nodes/%s/recover", strings.TrimSuffix(r.BaseURL, "/"), node.Name)
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		r.printf("[node-healing] recovery request to orchestrator failed for %s: %v\n", node.Name, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.printf("[node-healing] orchestrator recovery for %s returned %s\n", node.Name, resp.Status)
		return false
	}
	r.printf("[node-healing] requested orchestrator recovery for %s\n", node.Name)
	return true
}

// startBackgroundRetry is HEAL-01's own fix: re-attempts recovery for
// node on a bounded, backed-off schedule (RecoveryRetryPolicy) instead of
// giving up the instant the first attempt fails. Cancelled by a later
// OnTransition call for the same node (see cancelPendingLocked) - either
// because recovery already succeeded on a later attempt, the node
// recovered on its own, or a fresher transition superseded this one.
// Never blocks OnTransition's own caller - runs in its own goroutine.
func (r *OrchestratorReactor) startBackgroundRetry(node Node) {
	policy := r.RecoveryRetryPolicy
	if policy == (RetryPolicy{}) {
		policy = DefaultRecoveryRetryPolicy()
	}
	if err := policy.Validate(); err != nil {
		r.printf("[node-healing] invalid RecoveryRetryPolicy, not retrying recovery for %s: %v\n", node.Name, err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.pending == nil {
		r.pending = make(map[string]context.CancelFunc)
	}
	r.pending[node.Name] = cancel
	r.mu.Unlock()

	go func() {
		defer func() {
			// Only clear our own generation's bookkeeping: if we were
			// cancelled (ctx.Err() != nil), the caller that cancelled us
			// already owns cleanup (or has already installed a newer
			// entry we must not clobber).
			if ctx.Err() == nil {
				r.mu.Lock()
				delete(r.pending, node.Name)
				r.mu.Unlock()
			}
		}()
		for attempt := 1; attempt < policy.MaxAttempts; attempt++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(policy.Backoff(attempt)):
			}
			if ctx.Err() != nil {
				return
			}
			if r.attemptRecovery(node) {
				return
			}
		}
	}()
}
