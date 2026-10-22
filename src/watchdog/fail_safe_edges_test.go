// HYDRA-UMC-NODE-HEALING - watchdog package: fail_safe_edges_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The decisions that move work away from a node (or refuse to): the polling
// loop itself, the default recovery safeguards, the bounded retry, and every
// way a recovery request can be refused or fail. Each test asserts the
// decision, not just that the code runs.
package watchdog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	healthpb "github.com/JuanenRac/hydra-umc-node-healing/src/healthpb"
)

// countingTransport fails every request and counts how many were made.
type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, errors.New("connection refused")
}

func TestDefaultRecoveryPolicy_IsTheConservativeOne(t *testing.T) {
	p := DefaultRecoveryPolicy()
	if p.ConfirmAfter < 5*time.Second {
		t.Errorf("ConfirmAfter = %s, a single failed look must not be enough", p.ConfirmAfter)
	}
	if p.StartupGrace < p.ConfirmAfter {
		t.Errorf("StartupGrace = %s, must outlast the confirmation time", p.StartupGrace)
	}
	if p.Cooldown <= 0 || p.BudgetMax <= 0 || p.BudgetWindow <= 0 {
		t.Errorf("cooldown/budget must be active by default: %+v", p)
	}
	if !p.RequireDispatchNode {
		t.Error("a node without a dispatch node name must not be recovered by default")
	}
	if err := DefaultRecoveryRetryPolicy().Validate(); err != nil {
		t.Errorf("the default recovery retry policy is invalid: %v", err)
	}
}

func TestStatusString_NamesEveryClassification(t *testing.T) {
	want := map[Status]string{
		StatusUnknown: "UNKNOWN", StatusHealthy: "HEALTHY", StatusDegraded: "DEGRADED",
		StatusUnhealthy: "UNHEALTHY", StatusUnreachable: "UNREACHABLE", StatusInvalid: "INVALID",
		StatusMisconfigured: "MISCONFIGURED", Status(99): "UNKNOWN",
	}
	for s, name := range want {
		if s.String() != name {
			t.Errorf("Status(%d).String() = %q, want %q", int(s), s.String(), name)
		}
	}
}

func TestLogReactor_DetailFormattingAndZeroValueDoesNotPanic(t *testing.T) {
	var lines []string
	r := LogReactor{Printf: func(f string, a ...any) { lines = append(lines, strings.TrimSpace(fmt.Sprintf(f, a...))) }}
	r.OnTransition(Node{Name: "n"}, StatusHealthy, StatusUnreachable, "refused")
	r.OnTransition(Node{Name: "n"}, StatusUnreachable, StatusHealthy, "")
	if lines[0] != "[node-healing] n: HEALTHY -> UNREACHABLE (refused)" || lines[1] != "[node-healing] n: UNREACHABLE -> HEALTHY" {
		t.Fatalf("log lines = %q", lines)
	}
	LogReactor{}.OnTransition(Node{Name: "n"}, StatusUnknown, StatusHealthy, "")
}

func TestRun_PollsAtOnceThenEveryIntervalAndStopsWhenCancelled(t *testing.T) {
	addr, node, stop := startFakeNode(t, "svc")
	defer stop()
	reactor := &recordingReactor{}
	wd := NewWatchdog([]Node{{Name: "svc", Address: addr}}, reactor)
	wd.Interval = 20 * time.Millisecond
	wd.CheckTimeout = time.Second
	wd.RetryPolicy = RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()

	waitFor(t, "the first poll", func() bool { return len(reactor.snapshot()) == 1 })
	// a node that goes bad is noticed on a later tick, and reported once
	node.setState(healthpb.HealthState_HEALTH_STATE_UNHEALTHY)
	waitFor(t, "the next tick to notice the node going unhealthy", func() bool { return len(reactor.snapshot()) == 2 })
	time.Sleep(100 * time.Millisecond)
	got := reactor.snapshot()
	if len(got) != 2 || got[0] != "svc:UNKNOWN->HEALTHY" || got[1] != "svc:HEALTHY->UNHEALTHY" {
		t.Fatalf("transitions = %v, want exactly HEALTHY then UNHEALTHY (no per-tick repeats)", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func TestPollOne_ANilReactorStillRecordsStateWithoutPanicking(t *testing.T) {
	addr, _, stop := startFakeNode(t, "svc")
	defer stop()
	wd := NewWatchdog([]Node{{Name: "svc", Address: addr}}, nil)
	wd.pollAll(context.Background())
	wd.mu.Lock()
	defer wd.mu.Unlock()
	if wd.state["svc"] != StatusHealthy {
		t.Fatalf("state = %v, want HEALTHY", wd.state["svc"])
	}
}

func TestCheckNode_AnInvalidRetryPolicyFallsBackToTheDefaultAndStillRetries(t *testing.T) {
	rt := &countingTransport{}
	wd := &Watchdog{
		CheckTimeout: time.Second,
		RetryPolicy:  RetryPolicy{}, // invalid: zero attempts
		HTTPClient:   &http.Client{Transport: rt},
	}
	status, _ := wd.CheckOnce(context.Background(), Node{Name: "svc", HealthURL: "http://127.0.0.1:1/health"})
	if status != StatusUnreachable {
		t.Fatalf("status = %v, want UNREACHABLE", status)
	}
	if got, want := int(rt.calls.Load()), DefaultRetryPolicy().MaxAttempts; got != want {
		t.Fatalf("attempts = %d, want %d (the default policy, not zero and not unbounded)", got, want)
	}
}

func TestCheckNode_ARetryableFailureIsAttemptedExactlyMaxAttemptsTimes(t *testing.T) {
	rt := &countingTransport{}
	wd := &Watchdog{
		CheckTimeout: time.Second,
		RetryPolicy:  RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond},
		HTTPClient:   &http.Client{Transport: rt},
	}
	status, _ := wd.CheckOnce(context.Background(), Node{Name: "svc", HealthURL: "http://127.0.0.1:1/health"})
	if status != StatusUnreachable || rt.calls.Load() != 4 {
		t.Fatalf("status=%v attempts=%d, want UNREACHABLE after exactly 4", status, rt.calls.Load())
	}
}

func TestCheckNode_ACancelledContextEndsTheBackoffWithoutFurtherAttempts(t *testing.T) {
	rt := &countingTransport{}
	wd := &Watchdog{
		CheckTimeout: time.Second,
		RetryPolicy:  RetryPolicy{MaxAttempts: 5, BaseDelay: 10 * time.Second, MaxDelay: 10 * time.Second},
		HTTPClient:   &http.Client{Transport: rt},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	status, _ := wd.CheckOnce(ctx, Node{Name: "svc", HealthURL: "http://127.0.0.1:1/health"})
	if status != StatusUnreachable {
		t.Fatalf("status = %v, want UNREACHABLE", status)
	}
	if time.Since(start) > 2*time.Second || rt.calls.Load() != 1 {
		t.Fatalf("took %s with %d attempt(s): a shutdown must not wait out the 10s backoff", time.Since(start), rt.calls.Load())
	}
}

func TestCheckNode_AnUnusableHealthURLIsInvalidAndNeverRetried(t *testing.T) {
	rt := &countingTransport{}
	wd := &Watchdog{CheckTimeout: time.Second, RetryPolicy: DefaultRetryPolicy(), HTTPClient: &http.Client{Transport: rt}}
	status, detail := wd.CheckOnce(context.Background(), Node{Name: "svc", HealthURL: "http://bad\x7fhost/health"})
	if status != StatusInvalid || rt.calls.Load() != 0 {
		t.Fatalf("status=%v requests=%d detail=%q, want INVALID with no request", status, rt.calls.Load(), detail)
	}
}

func TestAttemptCheck_AnUnspecifiedGrpcStateIsDegradedNotHealthy(t *testing.T) {
	addr, node, stop := startFakeNode(t, "svc")
	defer stop()
	node.setState(healthpb.HealthState_HEALTH_STATE_UNSPECIFIED)
	wd := NewWatchdog([]Node{{Name: "svc", Address: addr}}, nil)
	status, _ := wd.CheckOnce(context.Background(), Node{Name: "svc", Address: addr})
	if status != StatusDegraded {
		t.Fatalf("status = %v, want DEGRADED", status)
	}
}

// ---- recovery requests ----

func TestAttemptRecovery_OutcomesForEveryAnswerTheOrchestratorCanGive(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   recoveryOutcome
		log    string
	}{
		{"ok with a count", 200, `{"requeuedMissions":["a"]}`, recoveryDone, "1 mission(s) requeued"},
		{"ok with a body that is not json", 200, `<html>`, recoveryDone, "requested orchestrator recovery"},
		{"unauthorized", 401, ``, recoveryRefused, "ORCHESTRATOR_TOKEN"},
		{"forbidden", 403, ``, recoveryRefused, "ORCHESTRATOR_TOKEN"},
		{"server error is worth retrying", 500, ``, recoveryTransient, "returned 500"},
		{"unknown node is worth retrying", 404, ``, recoveryTransient, "returned 404"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server, _ := countingServer(t, c.status, c.body)
			log := &lockedLog{}
			r := &OrchestratorReactor{BaseURL: server.URL, Printf: log.printf}
			if got := r.attemptRecovery(context.Background(), Node{Name: "svc"}, "cell-1"); got != c.want {
				t.Fatalf("outcome = %v, want %v", got, c.want)
			}
			if !strings.Contains(log.joined(), c.log) {
				t.Fatalf("log %q does not contain %q", log.joined(), c.log)
			}
		})
	}
}

func TestAttemptRecovery_AnUnbuildableRequestIsRefusedNotRetried(t *testing.T) {
	log := &lockedLog{}
	r := &OrchestratorReactor{BaseURL: "http://bad\x7fhost", Printf: log.printf}
	if got := r.attemptRecovery(context.Background(), Node{Name: "svc"}, "cell-1"); got != recoveryRefused {
		t.Fatalf("outcome = %v, want refused (retrying cannot fix a malformed address)", got)
	}
	if !strings.Contains(log.joined(), "could not be built") {
		t.Fatalf("log = %q", log.joined())
	}
}

func TestAttemptRecovery_AnUnreachableOrchestratorIsTransientAndSilentWhenShuttingDown(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close() // nothing listens any more
	log := &lockedLog{}
	r := &OrchestratorReactor{BaseURL: url, Printf: log.printf}
	if got := r.attemptRecovery(context.Background(), Node{Name: "svc"}, "c"); got != recoveryTransient {
		t.Fatalf("outcome = %v, want transient", got)
	}
	if !strings.Contains(log.joined(), "failed for svc") {
		t.Fatalf("a real failure was not logged: %q", log.joined())
	}

	quiet := &lockedLog{}
	r = &OrchestratorReactor{BaseURL: url, Printf: quiet.printf}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := r.attemptRecovery(ctx, Node{Name: "svc"}, "c"); got != recoveryTransient {
		t.Fatalf("outcome = %v, want transient", got)
	}
	if quiet.joined() != "" {
		t.Fatalf("a shutdown was logged as a failure: %q", quiet.joined())
	}
}

func TestRecoverNow_ARefusedRequestIsNeverRetriedAndATransientOneIsBounded(t *testing.T) {
	refused, refusedHits := countingServer(t, 401, ``)
	r := &OrchestratorReactor{BaseURL: refused.URL, Printf: func(string, ...any) {}, RecoveryRetryPolicy: fastRetryPolicy()}
	r.recoverNow(context.Background(), Node{Name: "a"}, "a")
	time.Sleep(200 * time.Millisecond)
	if refusedHits.Load() != 1 {
		t.Fatalf("a refused request was made %d times, want 1", refusedHits.Load())
	}

	broken, brokenHits := countingServer(t, 503, ``)
	r = &OrchestratorReactor{BaseURL: broken.URL, Printf: func(string, ...any) {}, RecoveryRetryPolicy: fastRetryPolicy()}
	r.recoverNow(context.Background(), Node{Name: "b"}, "b")
	max := int32(fastRetryPolicy().MaxAttempts)
	waitFor(t, "the retries to run out", func() bool { return brokenHits.Load() == max })
	time.Sleep(200 * time.Millisecond)
	if brokenHits.Load() != max {
		t.Fatalf("a dead orchestrator was asked %d times, want exactly %d (1 + retries), not forever", brokenHits.Load(), max)
	}
	waitFor(t, "the pending bookkeeping to be released", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.pending) == 0
	})
}

func TestRecoverNow_ACancelledContextRequestsNothing(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	r := &OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}, Policy: RecoveryPolicy{Cooldown: time.Hour}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.recoverNow(ctx, Node{Name: "svc"}, "svc")
	if hits.Load() != 0 {
		t.Fatalf("a recovery was requested after shutdown")
	}
	// and it must not have spent the cooldown on the aborted request
	if !r.allowRecovery("svc") {
		t.Fatal("the aborted request consumed the cooldown")
	}
}

func TestStartBackgroundRetry_AnInvalidPolicyIsReportedAndNothingIsScheduled(t *testing.T) {
	server, hits := countingServer(t, 503, ``)
	log := &lockedLog{}
	r := &OrchestratorReactor{
		BaseURL: server.URL, Printf: log.printf,
		RecoveryRetryPolicy: RetryPolicy{MaxAttempts: 3, BaseDelay: -time.Second, MaxDelay: time.Second},
	}
	r.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnreachable, "x")
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != 1 {
		t.Fatalf("requests = %d, want only the first synchronous attempt", hits.Load())
	}
	if !strings.Contains(log.joined(), "invalid RecoveryRetryPolicy") {
		t.Fatalf("the invalid policy was not reported: %q", log.joined())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) != 0 {
		t.Fatalf("pending = %d, want none", len(r.pending))
	}
}

func TestOnTransition_ShutdownCancelsAPendingConfirmation(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	r := &OrchestratorReactor{
		BaseURL: server.URL, Printf: func(string, ...any) {}, Context: ctx,
		Policy: RecoveryPolicy{ConfirmAfter: 80 * time.Millisecond},
	}
	r.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnreachable, "x")
	cancel()
	time.Sleep(200 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("a recovery was requested after the process began shutting down")
	}
}

func TestOnTransition_InvalidIdentityRequestsRecoveryOnTheDispatchNodesRoute(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
	}))
	defer server.Close()
	r := &OrchestratorReactor{BaseURL: server.URL + "/", Printf: func(string, ...any) {}}
	r.OnTransition(Node{Name: "svc", DispatchNode: "cell-9"}, StatusHealthy, StatusInvalid, "wrong name")
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "POST /nodes/cell-9/recover" {
		t.Fatalf("requests = %v, want one POST to the dispatch node's recover route", paths)
	}
}
