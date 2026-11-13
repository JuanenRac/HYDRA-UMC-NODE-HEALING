// HYDRA-UMC-NODE-HEALING - watchdog package: fleet_simulation_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A simulated fleet: the real watchdog polls simulated services over HTTP (steady, stuck, bouncing, coming back) and the real
// reactor asks a simulated orchestrator to recover them. Nothing leaves the machine and nothing real is touched; the point is that the
// safeguards (confirmation wait, budget, dispatch-node rule, retry) hold when the whole loop runs, not one function at a time.
package watchdog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// simService answers a health probe with 200 or 500 according to its mode; mode 2 flips on every request.
type simService struct {
	server *httptest.Server
	mode   atomic.Int32 // 0 healthy, 1 failing, 2 bouncing
	calls  atomic.Int64
}

func newSimService(t *testing.T, mode int32) *simService {
	t.Helper()
	s := &simService{}
	s.mode.Store(mode)
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.calls.Add(1)
		failing := s.mode.Load() == 1 || (s.mode.Load() == 2 && n%2 == 1)
		if failing {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(s.server.Close)
	return s
}

// simOrchestrator records the recover requests it gets, per dispatch node; the first `refuse` requests are answered 503.
type simOrchestrator struct {
	server   *httptest.Server
	mu       sync.Mutex
	byTarget map[string]int
	total    int
	refuse   int
	onRecove func(target string)
}

func newSimOrchestrator(t *testing.T, refuse int) *simOrchestrator {
	t.Helper()
	o := &simOrchestrator{byTarget: map[string]int{}, refuse: refuse}
	o.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/nodes/") || !strings.HasSuffix(r.URL.Path, "/recover") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		target := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/nodes/"), "/recover")
		o.mu.Lock()
		o.total++
		if o.refuse > 0 {
			o.refuse--
			o.mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		o.byTarget[target]++
		hook := o.onRecove
		o.mu.Unlock()
		if hook != nil {
			hook(target)
		}
		_, _ = w.Write([]byte(`{"requeuedMissions":[]}`))
	}))
	t.Cleanup(o.server.Close)
	return o
}

func (o *simOrchestrator) count(target string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.byTarget[target]
}

func (o *simOrchestrator) all() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.total
}

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuffer) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(strings.ReplaceAll(fmt.Sprintf(format, args...), "\n", " ")))
}

func (l *logBuffer) has(text string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, text) {
			return true
		}
	}
	return false
}

func fleetPolicy() RecoveryPolicy {
	return RecoveryPolicy{
		ConfirmAfter: 250 * time.Millisecond, Cooldown: time.Hour, BudgetMax: 10, BudgetWindow: time.Hour,
		RequireDispatchNode: true, RecoverUnhealthy: true,
	}
}

// runFleet polls the nodes with the reactor until the context is cancelled by the returned function.
func runFleet(t *testing.T, nodes []Node, reactor Reactor) (*Watchdog, func()) {
	t.Helper()
	wd := NewWatchdog(nodes, reactor)
	wd.Interval = 20 * time.Millisecond
	wd.CheckTimeout = 400 * time.Millisecond
	wd.RetryPolicy = RetryPolicy{MaxAttempts: 2, BaseDelay: 5 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()
	return wd, func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the watchdog did not stop")
		}
	}
}

func fleetWaitUntil(t *testing.T, limit time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stateOf(w *Watchdog, name string) Status {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state[name]
}

func TestFleet_AStuckNodeIsRecoveredOnceAndABouncingOneNever(t *testing.T) {
	stuck, bounce, fine := newSimService(t, 1), newSimService(t, 2), newSimService(t, 0)
	orchestrator := newSimOrchestrator(t, 0)
	logs := &logBuffer{}
	reactor := &OrchestratorReactor{BaseURL: orchestrator.server.URL, Printf: logs.printf, Policy: fleetPolicy()}
	wd, stop := runFleet(t, []Node{
		{Name: "stuck", HealthURL: stuck.server.URL, DispatchNode: "cell-1"},
		{Name: "bounce", HealthURL: bounce.server.URL, DispatchNode: "cell-2"},
		{Name: "fine", HealthURL: fine.server.URL, DispatchNode: "cell-3"},
	}, reactor)
	fleetWaitUntil(t, 5*time.Second, "the stuck node to be recovered", func() bool { return orchestrator.count("cell-1") == 1 })
	time.Sleep(600 * time.Millisecond) // long enough for a second request to show up if the safeguards were missing
	// The states are read while the watchdog still runs: stopping cancels a poll that is in flight, and that one reads as unreachable.
	fleetWaitUntil(t, 5*time.Second, "the healthy node to read healthy and the stuck one unhealthy", func() bool {
		return stateOf(wd, "fine") == StatusHealthy && stateOf(wd, "stuck") == StatusUnhealthy
	})
	finalFine, finalStuck := stateOf(wd, "fine"), stateOf(wd, "stuck")
	stop()
	if got := orchestrator.count("cell-1"); got != 1 {
		t.Errorf("the stuck node was recovered %d times, want exactly 1", got)
	}
	if got := orchestrator.count("cell-2"); got != 0 {
		t.Errorf("a node that bounces every poll was recovered %d times: the confirmation wait must restart on every change", got)
	}
	if got := orchestrator.count("cell-3"); got != 0 {
		t.Errorf("a healthy node was recovered %d times", got)
	}
	if finalFine != StatusHealthy {
		t.Errorf("the healthy node is %v", finalFine)
	}
	if finalStuck != StatusUnhealthy {
		t.Errorf("the stuck node is %v, want %v", finalStuck, StatusUnhealthy)
	}
}

func TestFleet_TheBudgetStopsARecoveryStorm(t *testing.T) {
	orchestrator := newSimOrchestrator(t, 0)
	logs := &logBuffer{}
	policy := fleetPolicy()
	policy.ConfirmAfter = 100 * time.Millisecond
	policy.BudgetMax = 2
	reactor := &OrchestratorReactor{BaseURL: orchestrator.server.URL, Printf: logs.printf, Policy: policy}
	var nodes []Node
	for _, name := range []string{"a", "b", "c", "d"} {
		nodes = append(nodes, Node{Name: name, HealthURL: newSimService(t, 1).server.URL, DispatchNode: "cell-" + name})
	}
	_, stop := runFleet(t, nodes, reactor)
	fleetWaitUntil(t, 5*time.Second, "two recoveries and the circuit breaker", func() bool { return orchestrator.all() >= 2 && logs.has("circuit breaker") })
	time.Sleep(300 * time.Millisecond)
	stop()
	if got := orchestrator.all(); got != 2 {
		t.Errorf("%d recovery requests with a budget of 2 for four failing nodes", got)
	}
	if got := reactor.RecoveryRequests(); got != 2 {
		t.Errorf("the reactor counted %d requests, want 2", got)
	}
}

func TestFleet_ANodeWithoutADispatchNodeIsOnlyLogged(t *testing.T) {
	orchestrator := newSimOrchestrator(t, 0)
	logs := &logBuffer{}
	reactor := &OrchestratorReactor{BaseURL: orchestrator.server.URL, Printf: logs.printf, Policy: fleetPolicy()}
	_, stop := runFleet(t, []Node{{Name: "service-only", HealthURL: newSimService(t, 1).server.URL}}, reactor)
	fleetWaitUntil(t, 5*time.Second, "the log line", func() bool { return logs.has("no \"dispatchNode\"") })
	time.Sleep(400 * time.Millisecond)
	stop()
	if got := orchestrator.all(); got != 0 {
		t.Errorf("a node with no dispatch node caused %d recovery requests", got)
	}
}

func TestFleet_AnOrchestratorThatIsBusyAtFirstIsReachedByTheRetry(t *testing.T) {
	orchestrator := newSimOrchestrator(t, 2) // the first two requests are answered 503
	logs := &logBuffer{}
	policy := fleetPolicy()
	policy.ConfirmAfter = 50 * time.Millisecond
	reactor := &OrchestratorReactor{
		BaseURL: orchestrator.server.URL, Printf: logs.printf, Policy: policy,
		RecoveryRetryPolicy: RetryPolicy{MaxAttempts: 5, BaseDelay: 10 * time.Millisecond, MaxDelay: 40 * time.Millisecond},
	}
	_, stop := runFleet(t, []Node{{Name: "stuck", HealthURL: newSimService(t, 1).server.URL, DispatchNode: "cell-1"}}, reactor)
	fleetWaitUntil(t, 8*time.Second, "the recovery to get through", func() bool { return orchestrator.count("cell-1") == 1 })
	time.Sleep(400 * time.Millisecond)
	stop()
	if got := orchestrator.count("cell-1"); got != 1 {
		t.Errorf("recovered %d times, want exactly once after the two refusals", got)
	}
	if got := orchestrator.all(); got != 3 {
		t.Errorf("the orchestrator saw %d requests, want 3 (two refused, one accepted)", got)
	}
}

func TestFleet_ANodeThatComesBackIsHealthyAgainAndNothingMoreIsAsked(t *testing.T) {
	service := newSimService(t, 1)
	orchestrator := newSimOrchestrator(t, 0)
	orchestrator.onRecove = func(string) { service.mode.Store(0) } // the recovery works: the service is up again
	reactor := &OrchestratorReactor{BaseURL: orchestrator.server.URL, Printf: (&logBuffer{}).printf, Policy: fleetPolicy()}
	wd, stop := runFleet(t, []Node{{Name: "svc", HealthURL: service.server.URL, DispatchNode: "cell-1"}}, reactor)
	fleetWaitUntil(t, 5*time.Second, "the node to be healthy after the recovery", func() bool {
		return orchestrator.count("cell-1") == 1 && stateOf(wd, "svc") == StatusHealthy
	})
	time.Sleep(500 * time.Millisecond)
	stop()
	if got := orchestrator.all(); got != 1 {
		t.Errorf("%d recovery requests for one failure", got)
	}
}

func TestFleet_ANodeThatDiesAndANodeThatLies(t *testing.T) {
	// a service that is gone is UNREACHABLE (and recovered); one that answers with another service's name is INVALID (and recovered)
	gone := newSimService(t, 0)
	goneURL := gone.server.URL
	gone.server.Close()
	liar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","name":"someone-else"}`))
	}))
	t.Cleanup(liar.Close)
	orchestrator := newSimOrchestrator(t, 0)
	policy := fleetPolicy()
	policy.ConfirmAfter = 100 * time.Millisecond
	reactor := &OrchestratorReactor{BaseURL: orchestrator.server.URL, Printf: (&logBuffer{}).printf, Policy: policy}
	wd, stop := runFleet(t, []Node{
		{Name: "gone", HealthURL: goneURL, DispatchNode: "cell-gone"},
		{Name: "liar", HealthURL: liar.URL, DispatchNode: "cell-liar"},
	}, reactor)
	fleetWaitUntil(t, 8*time.Second, "both to be recovered", func() bool { return orchestrator.count("cell-gone") == 1 && orchestrator.count("cell-liar") == 1 })
	// Read while the watchdog runs: stopping cancels a poll in flight, which reads as unreachable.
	fleetWaitUntil(t, 5*time.Second, "the dead node to read unreachable and the lying one invalid", func() bool {
		return stateOf(wd, "gone") == StatusUnreachable && stateOf(wd, "liar") == StatusInvalid
	})
	stop()
}
