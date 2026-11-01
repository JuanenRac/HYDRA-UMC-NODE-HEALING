// HYDRA-UMC-NODE-HEALING - watchdog package: lease_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOrchestrator keeps one lease the way the orchestrator does: one holder, a token that goes up with every new grant, and a
// recovery that is refused to anyone who does not show the current token.
type fakeOrchestrator struct {
	mu         sync.Mutex
	holder     string
	token      uint64
	nextToken  uint64
	down       bool
	recoveries []string
}

func (f *fakeOrchestrator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/leases/"):
			var body struct {
				Holder string `json:"holder"`
				Token  uint64 `json:"token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			switch action {
			case "acquire":
				if f.holder != "" && f.holder != body.Holder {
					w.WriteHeader(http.StatusConflict)
					return
				}
				if f.holder == "" {
					f.nextToken++
					f.token = f.nextToken
					f.holder = body.Holder
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"token": f.token})
			case "renew":
				if f.holder != body.Holder || f.token != body.Token {
					w.WriteHeader(http.StatusConflict)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"token": f.token})
			case "release":
				if f.holder == body.Holder && f.token == body.Token {
					f.holder = ""
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"released": true})
			}
		case strings.HasSuffix(r.URL.Path, "/recover"):
			if r.Header.Get("X-Healer-Id") != f.holder || r.Header.Get("X-Healer-Token") != strconv.FormatUint(f.token, 10) || r.Header.Get("X-Healer-Lease") != "healer-of-cell-1" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			f.recoveries = append(f.recoveries, r.URL.Path)
			_ = json.NewEncoder(w).Encode(map[string]any{"requeuedMissions": []string{}})
		}
	})
}

func newLease(url, holder string) *Lease {
	return &Lease{BaseURL: url, Cell: "cell-1", Holder: holder, TTL: time.Second, Printf: func(string, ...any) {}}
}

func TestLease_TheFirstWatchdogHoldsItAndTheSecondDoesNot(t *testing.T) {
	f := &fakeOrchestrator{}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	a, b := newLease(server.URL, "cm5"), newLease(server.URL, "devhost")
	a.Step(context.Background())
	b.Step(context.Background())
	if !a.Held() || b.Held() {
		t.Fatalf("held: a=%v b=%v, want a only", a.Held(), b.Held())
	}
	if a.Name() != "healer-of-cell-1" {
		t.Errorf("name = %q", a.Name())
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/nodes/n/recover", nil)
	if !a.Apply(req) || req.Header.Get("X-Healer-Id") != "cm5" || req.Header.Get("X-Healer-Token") == "" {
		t.Errorf("the holder must be able to show the lease, got %v", req.Header)
	}
	req2, _ := http.NewRequest(http.MethodPost, server.URL+"/nodes/n/recover", nil)
	if b.Apply(req2) {
		t.Error("a watchdog that does not hold the lease must not show one")
	}
}

func TestLease_ReleaseHandsItOverAtOnce(t *testing.T) {
	f := &fakeOrchestrator{}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	a, b := newLease(server.URL, "cm5"), newLease(server.URL, "devhost")
	a.Step(context.Background())
	a.Release()
	if a.Held() {
		t.Error("the lease must be gone after the release")
	}
	b.Step(context.Background())
	if !b.Held() {
		t.Error("the next watchdog must get the lease right after the release")
	}
}

func TestLease_AnOrchestratorThatCannotBeReachedMeansNotTheHolder(t *testing.T) {
	f := &fakeOrchestrator{}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	a := newLease(server.URL, "cm5")
	a.Step(context.Background())
	if !a.Held() {
		t.Fatal("expected to hold the lease")
	}
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	a.Step(context.Background())
	if a.Held() {
		t.Error("a renewal that was not confirmed must drop the claim")
	}
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	a.Step(context.Background())
	if !a.Held() {
		t.Error("the same holder asking again gets its lease back")
	}
}

func TestLease_RunKeepsRenewingAndReleasesWhenStopped(t *testing.T) {
	f := &fakeOrchestrator{}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	a := &Lease{BaseURL: server.URL, Cell: "cell-1", Holder: "cm5", TTL: 300 * time.Millisecond, Printf: func(string, ...any) {}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for !a.Held() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !a.Held() {
		t.Fatal("Run never took the lease")
	}
	time.Sleep(350 * time.Millisecond) // longer than a renewal interval: it must still be held
	if !a.Held() {
		t.Error("the lease must stay held while Run renews it")
	}
	cancel()
	<-done
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holder != "" {
		t.Errorf("Run must release the lease when stopped, holder = %q", f.holder)
	}
}

func TestOrchestratorReactor_RecoversOnlyWhileHoldingTheLease(t *testing.T) {
	f := &fakeOrchestrator{}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	holder, other := newLease(server.URL, "cm5"), newLease(server.URL, "devhost")
	holder.Step(context.Background())
	other.Step(context.Background())

	node := Node{Name: "node-a", DispatchNode: "node-a"}
	mine := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}, Lease: holder}
	mine.OnTransition(node, StatusHealthy, StatusUnreachable, "dial timeout")
	theirs := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}, Lease: other}
	theirs.OnTransition(node, StatusHealthy, StatusUnreachable, "dial timeout")

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recoveries) != 1 {
		t.Fatalf("recoveries = %v, want exactly one (from the holder)", f.recoveries)
	}
	if mine.RecoveryRequests() != 1 || theirs.RecoveryRequests() != 0 {
		t.Errorf("requests: holder %d (want 1), other %d (want 0): the one without the lease must not even ask", mine.RecoveryRequests(), theirs.RecoveryRequests())
	}
}

func TestOrchestratorReactor_AConflictWithALeaseIsNotRetried(t *testing.T) {
	var calls int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	lease := newLease(server.URL, "cm5")
	lease.held, lease.token = true, 7 // it thinks it holds the lease; the orchestrator says otherwise
	r := OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}, Lease: lease, RecoveryRetryPolicy: fastRetryPolicy()}
	r.OnTransition(Node{Name: "node-a", DispatchNode: "node-a"}, StatusHealthy, StatusUnreachable, "x")
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("calls = %d, want 1: a refusal because of the lease must not be retried", calls)
	}
}

func TestValidLabel(t *testing.T) {
	for _, ok := range []string{"a", "healer-of-cell_1", "cm5.local:1", strings.Repeat("x", 128)} {
		if !ValidLabel(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "a b", "a/b", strings.Repeat("x", 129), "añ"} {
		if ValidLabel(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}
