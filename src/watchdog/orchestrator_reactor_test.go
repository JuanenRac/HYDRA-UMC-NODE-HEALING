// HYDRA-UMC-NODE-HEALING - watchdog package: orchestrator_reactor_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package watchdog

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
