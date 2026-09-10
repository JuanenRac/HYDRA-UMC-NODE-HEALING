// HYDRA-UMC-NODE-HEALING - watchdog package: orchestrator_reactor_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package watchdog

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetryPolicy keeps HEAL-01's background-retry tests fast and
// deterministic without waiting out DefaultRecoveryRetryPolicy's real
// (multi-second) production backoff.
func fastRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 4, BaseDelay: 10 * time.Millisecond, MaxDelay: 40 * time.Millisecond}
}

func TestOrchestratorReactor_UnreachableTriggersRecovery(t *testing.T) {
	var gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	reactor := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusUnreachable, "dial timeout")

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/nodes/node-a/recover" {
		t.Errorf("path = %q, want /nodes/node-a/recover", gotPath)
	}
}

func TestOrchestratorReactor_InvalidTriggersRecovery(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	reactor := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "node-b"}, StatusHealthy, StatusInvalid, "identity mismatch")

	if !called {
		t.Error("expected a real recovery request for a StatusInvalid transition, got none")
	}
}

func TestOrchestratorReactor_HealthyTransitionNeverCallsOrchestrator(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	reactor := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "node-a"}, StatusUnreachable, StatusHealthy, "recovered")

	if called {
		t.Error("a node recovering to Healthy must never trigger a recovery request")
	}
}

func TestOrchestratorReactor_DegradedTransitionNeverCallsOrchestrator(t *testing.T) {
	// Degraded still means the node is alive and self-reporting - not the
	// "cannot be trusted at all" classification recovery exists for.
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	reactor := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusDegraded, "high latency")

	if called {
		t.Error("a Degraded transition must never trigger a recovery request")
	}
}

func TestOrchestratorReactor_SurvivesOrchestratorBeingUnreachable(t *testing.T) {
	// Points at a port nothing is listening on - the real failure mode of
	// Orchestrator being down. Must not panic.
	reactor := OrchestratorReactor{BaseURL: "http://127.0.0.1:1", Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusUnreachable, "dial timeout")
}

func TestOrchestratorReactor_DefaultClientHasARealTimeout(t *testing.T) {
	// http.DefaultClient (the old default) has no timeout at all, so a
	// hung Orchestrator - the most likely scenario during a real incident
	// - could block this call forever. A server that never responds
	// proves the default client itself now bounds the wait, without
	// relying on an injected Client (which was already always safe).
	// Sleeps well past the client's own timeout instead of blocking
	// forever (select{}) - the handler goroutine eventually returns and
	// lets the deferred server.Close() below complete, it just does so
	// after the assertion already ran.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(defaultRecoveryTimeout * 3)
	}))
	defer server.Close()

	reactor := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}

	done := make(chan struct{})
	go func() {
		reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusUnreachable, "dial timeout")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(defaultRecoveryTimeout * 2):
		t.Fatal("OnTransition did not return within the default client's own timeout - it is still blocking forever on a hung orchestrator")
	}
}

func TestOrchestratorReactor_AlwaysLogsRegardlessOfRecoveryOutcome(t *testing.T) {
	var logged bool
	reactor := OrchestratorReactor{
		BaseURL: "http://127.0.0.1:1", // unreachable - the HTTP call will fail
		Printf:  func(string, ...any) { logged = true },
	}
	reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusDegraded, "high latency")

	if !logged {
		t.Error("expected the real transition to be logged even when no recovery request is made")
	}
}

// HEAL-01 (P1):
// the real scenario from the finding - Orchestrator is unreachable at the
// exact moment of the transition, and the node itself never transitions
// again (it just stays down). Before the fix, nothing ever retried once
// Orchestrator came back; a single failed OnTransition call was the only
// chance recovery ever got.

func TestOrchestratorReactor_RetriesInBackgroundUntilOrchestratorComesBack(t *testing.T) {
	var attempts atomic.Int32
	// Fails the first 2 real requests (Orchestrator "still restarting"),
	// then succeeds - the exact "temporarily unavailable during the
	// transition, comes back later" scenario the finding describes.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}, RecoveryRetryPolicy: fastRetryPolicy()}

	start := time.Now()
	reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusUnreachable, "dial timeout")
	// OnTransition's own first attempt is synchronous (unchanged real
	// behavior) - it must return immediately after ONE failed attempt,
	// never block on the retry ladder itself.
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("OnTransition took %s - it must return after its own single synchronous attempt, not block on background retries", elapsed)
	}

	deadline := time.After(1 * time.Second)
	for {
		if attempts.Load() >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("background retry never reached a real successful attempt (got %d attempts)", attempts.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Give it a little longer to prove it does NOT keep retrying past the
	// real successful attempt - "una sola recuperacion efectiva".
	time.Sleep(80 * time.Millisecond)
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want exactly 3 (2 real failures + 1 real success, then stop)", got)
	}
}

func TestOrchestratorReactor_CancelsPendingRetryWhenNodeRecoversOnItsOwn(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable) // always fails - recovery must come from the node itself, not this server
	}))
	defer server.Close()

	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}, RecoveryRetryPolicy: fastRetryPolicy()}

	reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusUnreachable, "dial timeout")
	if attempts.Load() != 1 {
		t.Fatalf("attempts after the first transition = %d, want 1 (the real synchronous first attempt)", attempts.Load())
	}

	// The node itself recovers (a later, independent poll classifies it
	// Healthy again) before the background retry ladder gets anywhere -
	// the pending retry for the OLD Unreachable event must be cancelled,
	// not keep nagging Orchestrator about a node that is fine now.
	reactor.OnTransition(Node{Name: "node-a"}, StatusUnreachable, StatusHealthy, "recovered")

	time.Sleep(150 * time.Millisecond) // well past fastRetryPolicy's own full ladder
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts after the node recovered on its own = %d, want still 1 (the pending retry must have been cancelled)", got)
	}
}

func TestOrchestratorReactor_NewerUnreachableTransitionSupersedesAnOlderPendingRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}, RecoveryRetryPolicy: fastRetryPolicy()}

	// First Unreachable event (attempt 1, fails) starts a background retry.
	reactor.OnTransition(Node{Name: "node-a"}, StatusHealthy, StatusUnreachable, "dial timeout")
	// A second, distinct Unreachable event for the same node supersedes
	// the first's pending retry rather than racing it - exactly one
	// retry loop must be alive for this node at any time.
	reactor.OnTransition(Node{Name: "node-a"}, StatusUnreachable, StatusInvalid, "identity mismatch")

	deadline := time.After(1 * time.Second)
	for {
		// attempts.Add(1) <= 3 fails; the 4th real request (index 4)
		// succeeds - reachable only through the SECOND event's own retry
		// ladder actually running to a real success.
		if attempts.Load() >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the second transition's own retry never reached a real successful attempt (got %d attempts)", attempts.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}
