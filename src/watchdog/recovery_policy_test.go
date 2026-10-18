// HYDRA-UMC-NODE-HEALING - watchdog package: recovery_policy_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
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

// lockedLog collects reactor log lines safely across goroutines.
type lockedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *lockedLog) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l *lockedLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func countingServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRecoveryPolicy_NothingIsRequestedUntilTheNodeStaysDownForConfirmAfter(t *testing.T) {
	server, hits := countingServer(t, 200, `{"requeuedMissions":[]}`)
	reactor := &OrchestratorReactor{
		BaseURL: server.URL, Printf: func(string, ...any) {},
		Policy: RecoveryPolicy{ConfirmAfter: 80 * time.Millisecond},
	}
	reactor.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnreachable, "refused")

	time.Sleep(30 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("a recovery was requested before the confirmation time")
	}
	waitFor(t, "the confirmed recovery", func() bool { return hits.Load() == 1 })
}

func TestRecoveryPolicy_ANodeThatComesBackInsideTheWaitIsLeftAlone(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	reactor := &OrchestratorReactor{
		BaseURL: server.URL, Printf: func(string, ...any) {},
		Policy: RecoveryPolicy{ConfirmAfter: 60 * time.Millisecond},
	}
	reactor.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnreachable, "blip")
	time.Sleep(20 * time.Millisecond)
	reactor.OnTransition(Node{Name: "svc"}, StatusUnreachable, StatusHealthy, "")

	time.Sleep(150 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("a flapping node got %d recovery request(s), want none", hits.Load())
	}
}

func TestRecoveryPolicy_StartupGraceDelaysTheFirstRecoveries(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	reactor := &OrchestratorReactor{
		BaseURL: server.URL, Printf: func(string, ...any) {},
		Policy: RecoveryPolicy{ConfirmAfter: time.Millisecond, StartupGrace: 120 * time.Millisecond},
	}
	reactor.OnTransition(Node{Name: "svc"}, StatusUnknown, StatusUnreachable, "not started yet")
	time.Sleep(50 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatalf("a recovery was requested during the startup grace period")
	}
	waitFor(t, "the recovery after the grace period", func() bool { return hits.Load() == 1 })
}

func TestRecoveryPolicy_CooldownAndBudgetStopRepeatedRequests(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	log := &lockedLog{}
	var clock atomic.Int64
	clock.Store(1000)
	reactor := &OrchestratorReactor{
		BaseURL: server.URL, Printf: log.printf,
		Now:    func() time.Time { return time.Unix(clock.Load(), 0) },
		Policy: RecoveryPolicy{Cooldown: time.Minute, BudgetMax: 2, BudgetWindow: time.Hour},
	}
	down := func(name string) {
		reactor.OnTransition(Node{Name: name}, StatusHealthy, StatusUnreachable, "x")
		reactor.OnTransition(Node{Name: name}, StatusUnreachable, StatusHealthy, "")
	}

	down("a")
	down("a") // same node again within the cooldown
	if hits.Load() != 1 {
		t.Fatalf("after a repeated failure of one node: %d request(s), want 1 (cooldown)", hits.Load())
	}
	down("b")
	down("c") // third node: past the budget of 2
	if hits.Load() != 2 {
		t.Fatalf("after three nodes failed: %d request(s), want 2 (budget)", hits.Load())
	}
	if !strings.Contains(log.joined(), "circuit breaker") || !strings.Contains(log.joined(), "cooldown") {
		t.Fatalf("the skipped requests were not explained in the log:\n%s", log.joined())
	}

	clock.Add(2 * 3600) // window and cooldown over
	down("c")
	if hits.Load() != 3 {
		t.Fatalf("after the window passed: %d request(s), want 3", hits.Load())
	}
}

func TestRecoveryPolicy_ANodeWithoutADispatchNodeIsOnlyLogged(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	log := &lockedLog{}
	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: log.printf, Policy: RecoveryPolicy{RequireDispatchNode: true}}

	reactor.OnTransition(Node{Name: "HYDRA-UMC-VISION-NODE"}, StatusHealthy, StatusUnreachable, "refused")
	if hits.Load() != 0 || !strings.Contains(log.joined(), "dispatchNode") {
		t.Fatalf("hits=%d log=%q, want no request and an explanation", hits.Load(), log.joined())
	}

	var path atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { path.Store(r.URL.Path) }))
	defer other.Close()
	reactor = &OrchestratorReactor{BaseURL: other.URL, Printf: func(string, ...any) {}, Policy: RecoveryPolicy{RequireDispatchNode: true}}
	reactor.OnTransition(Node{Name: "HYDRA-UMC-VISION-NODE", DispatchNode: "cell-2"}, StatusHealthy, StatusUnreachable, "refused")
	if path.Load() != "/nodes/cell-2/recover" {
		t.Fatalf("recover path = %v, want the dispatch node name", path.Load())
	}
}

func TestRecoveryPolicy_AMisconfiguredNodeNeverTriggersRecovery(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "svc"}, StatusUnknown, StatusMisconfigured, "401")
	if hits.Load() != 0 {
		t.Fatalf("a configuration problem triggered a recovery")
	}
}

func TestRecoveryPolicy_UnhealthyRecoversOnlyWhenEnabled(t *testing.T) {
	server, hits := countingServer(t, 200, `{}`)
	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnhealthy, "503")
	if hits.Load() != 0 {
		t.Fatalf("UNHEALTHY recovered although RecoverUnhealthy is off")
	}
	reactor.Policy.RecoverUnhealthy = true
	reactor.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnhealthy, "503")
	if hits.Load() != 1 {
		t.Fatalf("UNHEALTHY did not recover although RecoverUnhealthy is on")
	}
}

func TestRecoveryPolicy_ARefusedTokenIsReportedOnceAndNotRetried(t *testing.T) {
	server, hits := countingServer(t, http.StatusUnauthorized, ``)
	log := &lockedLog{}
	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: log.printf, RecoveryRetryPolicy: fastRetryPolicy()}
	reactor.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnreachable, "x")
	time.Sleep(150 * time.Millisecond)
	if hits.Load() != 1 {
		t.Fatalf("a 401 was requested %d times, want exactly 1", hits.Load())
	}
	if !strings.Contains(log.joined(), "ORCHESTRATOR_TOKEN") {
		t.Fatalf("the log does not say what to fix:\n%s", log.joined())
	}
}

func TestRecoveryPolicy_TheNumberOfRequeuedMissionsIsLogged(t *testing.T) {
	server, _ := countingServer(t, 200, `{"requeuedMissions":["m1","m2"]}`)
	log := &lockedLog{}
	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: log.printf}
	reactor.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnreachable, "x")
	if !strings.Contains(log.joined(), "2 mission(s) requeued") {
		t.Fatalf("log does not report the requeued missions:\n%s", log.joined())
	}
}

func TestHTTPProbe_APersistenceProblemIsDegradedAndCredentialsStayOutOfMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"status":"ok","name":"queue","persistenceHealthy":false}`))
	}))
	defer srv.Close()
	got, detail := probeWatchdog().CheckOnce(context.Background(), Node{Name: "queue", HealthURL: srv.URL})
	if got != StatusDegraded || detail == "" {
		t.Fatalf("status = %s (%q), want DEGRADED with an explanation", got, detail)
	}

	missing := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(404) }))
	defer missing.Close()
	withSecret := strings.Replace(missing.URL, "http://", "http://operator:s3cr3t@", 1)
	_, detail = probeWatchdog().CheckOnce(context.Background(), Node{Name: "queue", HealthURL: withSecret})
	if strings.Contains(detail, "s3cr3t") {
		t.Fatalf("the password leaked into %q", detail)
	}
}
