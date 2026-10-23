// HYDRA-UMC-NODE-HEALING - watchdog package: orchestrator_reactor.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
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
// watchdog's own Node.Name (the SERVICE being watched) and Orchestrator's own
// mission-dispatch node names (whatever a caller passed to
// POST /missions/:id/dispatch) are different name spaces. The registry's
// "dispatchNode" says which dispatch node a watched service stands for; this
// reactor does not guess a translation between the two.
package watchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// defaultRecoveryTimeout bounds the recovery POST below when no Client is
// given. http.DefaultClient has no timeout at all, so if Orchestrator itself is
// hung - the most likely scenario during a real incident - this call could
// block forever per unhealthy-node transition, leaking a goroutine exactly
// when the system is most compromised.
const defaultRecoveryTimeout = 5 * time.Second

// DefaultRecoveryRetryPolicy bounds this project's own background retry (see
// startBackgroundRetry): up to 5 attempts, starting at 2s and capped at
// 30s, so a briefly-restarting Orchestrator is still reached without
// hammering it, and a genuinely long-term outage stops retrying instead
// of running forever. Reuses RetryPolicy - the same deterministic,
// directly-assertable backoff already used for per-tick node checks -
// rather than inventing a second backoff shape for the same math.
func DefaultRecoveryRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 5, BaseDelay: 2 * time.Second, MaxDelay: 30 * time.Second}
}

// RecoveryPolicy holds the safeguards between "a node looks down" and "its
// missions are requeued". A recovery moves work to another cell while the
// node may still be running it, so it needs more than one failed look.
// The zero value keeps the original behavior (act on the transition at once,
// for any node name) so the type stays easy to drive from tests; main.go sets
// the safe values below explicitly.
type RecoveryPolicy struct {
	// ConfirmAfter is how long a node must stay in its failing state, without
	// any other transition, before recovery is requested. A node that comes
	// back, or changes state again, inside this time is left alone.
	ConfirmAfter time.Duration
	// StartupGrace delays the first recoveries after this process starts: at
	// boot the watched services are often not up yet and every one of them
	// would look down.
	StartupGrace time.Duration
	// Cooldown is the minimum time between two recovery requests for the same node.
	Cooldown time.Duration
	// BudgetMax recovery requests are allowed per BudgetWindow across all
	// nodes; past it the reactor only logs (a circuit breaker for a flapping
	// network that would otherwise requeue everything).
	BudgetMax    int
	BudgetWindow time.Duration
	// RequireDispatchNode makes Node.DispatchNode mandatory: a node without
	// one is only logged, because Node.Name is a service name, not a dispatch node.
	RequireDispatchNode bool
	// RecoverUnhealthy also treats a node that answers but reports itself
	// unhealthy as recoverable; otherwise only UNREACHABLE and INVALID are.
	RecoverUnhealthy bool
}

// DefaultRecoveryPolicy is what the binary uses unless a flag overrides it.
func DefaultRecoveryPolicy() RecoveryPolicy {
	return RecoveryPolicy{
		ConfirmAfter:        15 * time.Second,
		StartupGrace:        60 * time.Second,
		Cooldown:            5 * time.Minute,
		BudgetMax:           3,
		BudgetWindow:        10 * time.Minute,
		RequireDispatchNode: true,
		RecoverUnhealthy:    true,
	}
}

// OrchestratorReactor wraps LogReactor's own real logging (an operator
// must still see every transition via journalctl regardless of whether
// the HTTP call below succeeds) with a real recovery request to
// HYDRA-UMC-ORCHESTRATOR for the classifications that mean a node's
// in-flight work needs to be requeued elsewhere (see RecoveryPolicy for the
// safeguards in front of that request).
//
// The recovery request is retried in the background, on a bounded schedule
// (see startBackgroundRetry), so an Orchestrator that is briefly down at the
// moment of the transition is still reached. Use `&OrchestratorReactor{...}`
// (a pointer), not a bare value - the pending-retry bookkeeping below needs
// one real, shared instance, the same discipline Watchdog itself already uses
// for its own state map.
type OrchestratorReactor struct {
	// BaseURL is Orchestrator's own HTTP API base, e.g.
	// "http://127.0.0.1:8114" - no trailing slash required.
	BaseURL string
	// Client defaults to a client bounded by defaultRecoveryTimeout, not
	// http.DefaultClient (which has no timeout) - overridable for tests.
	Client *http.Client
	// Token, when set, is sent as "Authorization: Bearer <token>" - an
	// orchestrator started with a token (or on a network address, where it
	// must have one) answers 401 to a recovery request without it.
	Token string
	// Printf defaults to fmt.Printf, same as LogReactor's own default.
	Printf func(format string, args ...any)
	// RecoveryRetryPolicy bounds the background retry below - defaults to
	// DefaultRecoveryRetryPolicy() when left zero-valued.
	RecoveryRetryPolicy RetryPolicy
	// Policy holds the safeguards in front of every recovery request.
	Policy RecoveryPolicy
	// Context, when set, ends every pending and future recovery when it is
	// cancelled (the process is shutting down); nil means context.Background().
	Context context.Context
	// Now defaults to time.Now - overridable for tests.
	Now func() time.Time

	requests atomic.Int64 // recovery POSTs sent, whatever the answer

	mu         sync.Mutex
	pending    map[string]context.CancelFunc // node name -> cancel for its in-flight confirmation or retry, if any
	startedAt  time.Time
	lastByNode map[string]time.Time
	recent     []time.Time // times of recovery requests inside the budget window
}

func (r *OrchestratorReactor) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *OrchestratorReactor) rootContext() context.Context {
	if r.Context != nil {
		return r.Context
	}
	return context.Background()
}

func (r *OrchestratorReactor) printf(format string, args ...any) {
	printf := r.Printf
	if printf == nil {
		printf = func(format string, args ...any) { fmt.Printf(format, args...) }
	}
	printf(format, args...)
}

// recoverable says whether a transition into status can ask for a recovery.
// A misconfigured health URL (a 3xx/4xx) says nothing about the node, so it
// never does.
func (r *OrchestratorReactor) recoverable(to Status) bool {
	switch to {
	case StatusUnreachable, StatusInvalid:
		return true
	case StatusUnhealthy:
		return r.Policy.RecoverUnhealthy
	}
	return false
}

func (r *OrchestratorReactor) OnTransition(node Node, from, to Status, detail string) {
	// Real logging first, unconditionally - the same visibility
	// LogReactor already provides must never regress just because this
	// reactor also tries something the log-only one didn't.
	LogReactor{Printf: r.Printf}.OnTransition(node, from, to, detail)

	r.mu.Lock()
	if r.startedAt.IsZero() {
		r.startedAt = r.now()
	}
	r.mu.Unlock()

	// Any earlier confirmation or background retry for this exact node is no
	// longer relevant to THIS transition: the node recovered on its own, or a
	// fresh failing event should restart the wait. Cancelling first,
	// unconditionally, keeps exactly one pending action per node alive.
	r.cancelPendingLocked(node.Name)

	if !r.recoverable(to) {
		return
	}

	target := node.Name
	if node.DispatchNode != "" {
		target = node.DispatchNode
	} else if r.Policy.RequireDispatchNode {
		r.printf("[node-healing] %s is %s, but it has no \"dispatchNode\" in the registry, so no recovery is requested for it\n", node.Name, to)
		return
	}

	delay := r.Policy.ConfirmAfter
	if r.Policy.StartupGrace > 0 {
		r.mu.Lock()
		remaining := r.startedAt.Add(r.Policy.StartupGrace).Sub(r.now())
		r.mu.Unlock()
		if remaining > delay {
			delay = remaining
		}
	}
	if delay <= 0 {
		r.recoverNow(r.rootContext(), node, target)
		return
	}

	ctx, cancel := context.WithCancel(r.rootContext())
	r.mu.Lock()
	if r.pending == nil {
		r.pending = make(map[string]context.CancelFunc)
	}
	r.pending[node.Name] = cancel
	r.mu.Unlock()
	r.printf("[node-healing] %s is %s; recovery will be requested only if it is still %s in %s\n", node.Name, to, to, delay.Round(time.Second))
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		r.recoverNow(ctx, node, target)
	}()
}

func (r *OrchestratorReactor) cancelPendingLocked(nodeName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.pending[nodeName]; ok {
		cancel()
		delete(r.pending, nodeName)
	}
}

// allowRecovery applies the cooldown and the global budget, and records the
// request when it is allowed.
func (r *OrchestratorReactor) allowRecovery(nodeName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if cooldown := r.Policy.Cooldown; cooldown > 0 {
		if last, ok := r.lastByNode[nodeName]; ok && now.Sub(last) < cooldown {
			r.printf("[node-healing] recovery for %s skipped: the last request was %s ago (cooldown %s)\n", nodeName, now.Sub(last).Round(time.Second), cooldown)
			return false
		}
	}
	if r.Policy.BudgetMax > 0 && r.Policy.BudgetWindow > 0 {
		kept := r.recent[:0]
		for _, at := range r.recent {
			if now.Sub(at) < r.Policy.BudgetWindow {
				kept = append(kept, at)
			}
		}
		r.recent = kept
		if len(r.recent) >= r.Policy.BudgetMax {
			r.printf("[node-healing] recovery for %s skipped: %d requests were already made in the last %s (circuit breaker; only logging until it clears)\n", nodeName, len(r.recent), r.Policy.BudgetWindow)
			return false
		}
		r.recent = append(r.recent, now)
	}
	if r.lastByNode == nil {
		r.lastByNode = make(map[string]time.Time)
	}
	r.lastByNode[nodeName] = now
	return true
}

// recoverNow makes the first recovery request (subject to the cooldown and
// the budget) and, when it fails for a transient reason, keeps trying in the
// background.
func (r *OrchestratorReactor) recoverNow(ctx context.Context, node Node, target string) {
	if ctx.Err() != nil || !r.allowRecovery(node.Name) {
		return
	}
	switch r.attemptRecovery(ctx, node, target) {
	case recoveryDone, recoveryRefused:
		return
	}
	r.startBackgroundRetry(node, target)
}

// RecoveryRequests is how many recovery requests were sent to the Orchestrator, whatever the answer.
func (r *OrchestratorReactor) RecoveryRequests() int64 { return r.requests.Load() }

type recoveryOutcome int

const (
	recoveryDone      recoveryOutcome = iota
	recoveryTransient                 // worth another try
	recoveryRefused                   // will not get better by retrying (wrong token, bad request)
)

// attemptRecovery makes exactly one real recovery POST and logs the
// outcome, including how many missions the Orchestrator requeued.
func (r *OrchestratorReactor) attemptRecovery(ctx context.Context, node Node, target string) recoveryOutcome {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: defaultRecoveryTimeout}
	}
	endpoint := fmt.Sprintf("%s/nodes/%s/recover", strings.TrimSuffix(r.BaseURL, "/"), url.PathEscape(target))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		r.printf("[node-healing] recovery request to orchestrator could not be built for %s: %v\n", node.Name, err)
		return recoveryRefused
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	r.requests.Add(1)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			r.printf("[node-healing] recovery request to orchestrator failed for %s: %v\n", node.Name, err)
		}
		return recoveryTransient
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		r.printf("[node-healing] orchestrator refused the recovery for %s (%s): ORCHESTRATOR_TOKEN is missing or wrong, so it will not be retried\n", node.Name, resp.Status)
		return recoveryRefused
	}
	if resp.StatusCode != http.StatusOK {
		r.printf("[node-healing] orchestrator recovery for %s returned %s\n", node.Name, resp.Status)
		return recoveryTransient
	}
	var answer struct {
		RequeuedMissions []json.RawMessage `json:"requeuedMissions"`
	}
	if raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64*1024)); rerr == nil && json.Unmarshal(raw, &answer) == nil {
		r.printf("[node-healing] requested orchestrator recovery for %s (dispatch node %s): %d mission(s) requeued\n", node.Name, target, len(answer.RequeuedMissions))
		return recoveryDone
	}
	r.printf("[node-healing] requested orchestrator recovery for %s (dispatch node %s)\n", node.Name, target)
	return recoveryDone
}

// startBackgroundRetry re-attempts recovery on a bounded, backed-off schedule
// (RecoveryRetryPolicy) instead of giving up the instant the first attempt
// fails. It stops when a later transition for the same node cancels it, when
// the process shuts down, when an attempt succeeds, or when the Orchestrator
// refuses the request for a reason a retry cannot fix.
func (r *OrchestratorReactor) startBackgroundRetry(node Node, target string) {
	policy := r.RecoveryRetryPolicy
	if policy == (RetryPolicy{}) {
		policy = DefaultRecoveryRetryPolicy()
	}
	if err := policy.Validate(); err != nil {
		r.printf("[node-healing] invalid RecoveryRetryPolicy, not retrying recovery for %s: %v\n", node.Name, err)
		return
	}

	ctx, cancel := context.WithCancel(r.rootContext())
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
			cancel()
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
			if r.attemptRecovery(ctx, node, target) != recoveryTransient {
				return
			}
		}
	}()
}
