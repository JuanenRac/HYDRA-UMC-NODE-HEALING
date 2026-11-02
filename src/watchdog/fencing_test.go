// HYDRA-UMC-NODE-HEALING - watchdog package: fencing_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Fencing is safety: the tests insist on the refusals. A cell that does not answer, answers badly, answers late or says a state that
// is not on the list must never let a recovery request leave.
package watchdog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCell is a cell that holds robots: POST /robots/{node}/hold and GET /robots/{node}/state.
type fakeCell struct {
	mu         sync.Mutex
	state      string
	holdStatus int
	holds      int
	autoStop   bool // the hold changes the state to "stopped"
	badBody    bool
	token      string
	seenToken  string
}

func (c *fakeCell) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.seenToken = r.Header.Get("Authorization")
		if c.token != "" && c.seenToken != "Bearer "+c.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/hold"):
			c.holds++
			if c.holdStatus != 0 {
				w.WriteHeader(c.holdStatus)
				return
			}
			if c.autoStop {
				c.state = "stopped"
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state"):
			if c.badBody {
				_, _ = w.Write([]byte("not json"))
				return
			}
			_, _ = w.Write([]byte(`{"state":"` + c.state + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func confirmConfig(base string) FencingConfig {
	return FencingConfig{Enabled: true, Mode: "confirm", HoldURL: base + "/robots/{node}/hold", StatusURL: base + "/robots/{node}/state", ConfirmTimeoutSeconds: 1, PollMillis: 100}
}

func newFencer(t *testing.T, c FencingConfig) *HTTPFencer {
	t.Helper()
	f, err := NewHTTPFencer(c)
	if err != nil {
		t.Fatalf("NewHTTPFencer: %v", err)
	}
	return f
}

func TestFencing_ConfirmedWhenTheCellHoldsTheRobot(t *testing.T) {
	cell := &fakeCell{state: "moving", autoStop: true}
	server := httptest.NewServer(cell.handler())
	defer server.Close()
	verdict := newFencer(t, confirmConfig(server.URL)).Fence(context.Background(), Node{Name: "n"}, "cell-a", 0)
	if !verdict.Safe {
		t.Fatalf("a robot the cell reports stopped must be safe: %s", verdict.Reason)
	}
	if cell.holds != 1 {
		t.Errorf("holds = %d, want 1", cell.holds)
	}
}

func TestFencing_StateIsPolledUntilItIsSafe(t *testing.T) {
	cell := &fakeCell{state: "moving"}
	server := httptest.NewServer(cell.handler())
	defer server.Close()
	go func() {
		time.Sleep(250 * time.Millisecond)
		cell.mu.Lock()
		cell.state = "Held" // the case of the word is not important
		cell.mu.Unlock()
	}()
	c := confirmConfig(server.URL)
	c.ConfirmTimeoutSeconds = 3
	if verdict := newFencer(t, c).Fence(context.Background(), Node{Name: "n"}, "cell-a", 0); !verdict.Safe {
		t.Fatalf("expected safe after the robot is held: %s", verdict.Reason)
	}
}

func TestFencing_EveryDoubtIsARefusal(t *testing.T) {
	cases := map[string]func(*fakeCell){
		"still moving":       func(c *fakeCell) { c.state = "moving" },
		"unknown state":      func(c *fakeCell) { c.state = "maybe" },
		"hold refused":       func(c *fakeCell) { c.state = "stopped"; c.holdStatus = http.StatusInternalServerError },
		"hold not allowed":   func(c *fakeCell) { c.state = "stopped"; c.holdStatus = http.StatusForbidden },
		"state not json":     func(c *fakeCell) { c.badBody = true },
		"wrong token":        func(c *fakeCell) { c.state = "stopped"; c.token = "another-token-another" },
		"empty state string": func(c *fakeCell) { c.state = "" },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			cell := &fakeCell{}
			setup(cell)
			server := httptest.NewServer(cell.handler())
			defer server.Close()
			c := confirmConfig(server.URL)
			c.ConfirmTimeoutSeconds = 1
			verdict := newFencer(t, c).Fence(context.Background(), Node{Name: "n"}, "cell-a", time.Hour)
			if verdict.Safe {
				t.Fatalf("%s must not be safe", name)
			}
			if verdict.Reason == "" {
				t.Error("a refusal must say why")
			}
		})
	}
}

func TestFencing_AnUnreachableCellIsARefusal(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	base := server.URL
	server.Close() // nothing listens any more
	c := confirmConfig(base)
	c.ConfirmTimeoutSeconds = 1
	if verdict := newFencer(t, c).Fence(context.Background(), Node{Name: "n"}, "cell-a", time.Hour); verdict.Safe {
		t.Fatal("a cell that cannot be reached cannot confirm anything")
	}
}

func TestFencing_AStoppedContextIsARefusal(t *testing.T) {
	cell := &fakeCell{state: "moving"}
	server := httptest.NewServer(cell.handler())
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if verdict := newFencer(t, confirmConfig(server.URL)).Fence(ctx, Node{Name: "n"}, "cell-a", 0); verdict.Safe {
		t.Fatal("a cancelled context must not confirm anything")
	}
}

func TestFencing_TheTokenIsSentAndTheNodeIsEscaped(t *testing.T) {
	cell := &fakeCell{state: "stopped", token: "0123456789abcdef-token"}
	var paths []string
	var mu sync.Mutex
	inner := cell.handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.EscapedPath())
		mu.Unlock()
		inner.ServeHTTP(w, r)
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("0123456789abcdef-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := confirmConfig(server.URL)
	c.TokenFile = tokenFile
	if verdict := newFencer(t, c).Fence(context.Background(), Node{Name: "n"}, "cell a/../b", 0); !verdict.Safe {
		t.Fatalf("safe expected: %s", verdict.Reason)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if strings.Contains(p, "/../") || strings.Count(p, "/") != 3 {
			t.Errorf("the dispatch node was not escaped in %q", p)
		}
	}
}

func TestFencing_StatusOnlyModeNeedsNoHold(t *testing.T) {
	cell := &fakeCell{state: "isolated"}
	server := httptest.NewServer(cell.handler())
	defer server.Close()
	c := confirmConfig(server.URL)
	c.HoldURL = ""
	if verdict := newFencer(t, c).Fence(context.Background(), Node{Name: "n"}, "cell-a", 0); !verdict.Safe || cell.holds != 0 {
		t.Fatalf("a cell that reports isolated is safe without a hold (holds=%d): %s", cell.holds, verdict.Reason)
	}
}

func TestFencing_LeaseModeWaitsForTheLeaseToExpire(t *testing.T) {
	margin := 2
	f := newFencer(t, FencingConfig{Enabled: true, Mode: "lease", LeaseTtlSeconds: 10, MarginSeconds: &margin})
	if v := f.Fence(context.Background(), Node{Name: "n"}, "c", 11*time.Second); v.Safe {
		t.Error("11 s of failure is not more than the lease of 10 s plus 2 s of margin")
	}
	if v := f.Fence(context.Background(), Node{Name: "n"}, "c", 0); v.Safe {
		t.Error("a node whose failure time is unknown (0) cannot be fenced by lease")
	}
	if v := f.Fence(context.Background(), Node{Name: "n"}, "c", 13*time.Second); !v.Safe {
		t.Errorf("13 s of failure is past the lease: %s", v.Reason)
	}
}

func TestFencingConfig_Validation(t *testing.T) {
	bad := map[string]FencingConfig{
		"confirm without a status url": {Enabled: true},
		"unknown mode":                 {Enabled: true, Mode: "trust", StatusURL: "http://127.0.0.1/x"},
		"lease without a ttl":          {Enabled: true, Mode: "lease"},
		"plain http to a remote cell":  {Enabled: true, StatusURL: "http://cell.example/robots/{node}/state"},
		"not a url":                    {Enabled: true, StatusURL: "ftp://127.0.0.1/x"},
		"node in the host":             {Enabled: true, StatusURL: "http://{node}.local/state", AllowPlaintext: true},
		"timeout too long":             {Enabled: true, StatusURL: "http://127.0.0.1/x", ConfirmTimeoutSeconds: 999},
		"poll too fast":                {Enabled: true, StatusURL: "http://127.0.0.1/x", PollMillis: 5},
		"upper case safe state":        {Enabled: true, StatusURL: "http://127.0.0.1/x", SafeStates: []string{"Stopped"}},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	good := []FencingConfig{{}, {Enabled: true, StatusURL: "http://127.0.0.1:8080/x/{node}"}, {Enabled: true, StatusURL: "https://cell.example/x", HoldURL: "https://cell.example/h"},
		{Enabled: true, Mode: "lease", LeaseTtlSeconds: 5}, {Enabled: true, StatusURL: "http://cell.example/x", AllowPlaintext: true}}
	for i, c := range good {
		if err := c.Validate(); err != nil {
			t.Errorf("good config %d refused: %v", i, err)
		}
	}
}

func TestLoadFencingConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadFencingConfig(write("a.json", `{"enabled": true, "statusUrl": "http://127.0.0.1:1/x", "confirmTimeoutSecond": 3}`)); err == nil {
		t.Error("a misspelt key must be refused")
	}
	if _, err := LoadFencingConfig(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("a missing file must be refused")
	}
	c, err := LoadFencingConfig(write("ok.json", `{"enabled": false}`))
	if err != nil || c.Enabled {
		t.Errorf("a file that leaves it off must load and stay off: %v", err)
	}
	if _, err := NewHTTPFencer(FencingConfig{Enabled: true, StatusURL: "http://127.0.0.1/x", TokenFile: write("short", "abc")}); err == nil {
		t.Error("a short token must be refused")
	}
}

// refuser and approver stand in for a Fencer.
type fixedFencer struct {
	safe  bool
	calls atomic.Int64
	seen  atomic.Int64 // failingFor in milliseconds of the last call
}

func (f *fixedFencer) Fence(_ context.Context, _ Node, _ string, failingFor time.Duration) FenceResult {
	f.calls.Add(1)
	f.seen.Store(failingFor.Milliseconds())
	if f.safe {
		return FenceResult{true, "stopped"}
	}
	return FenceResult{false, "the cell did not answer"}
}

func recoverServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/recover") {
			posts.Add(1)
		}
		_, _ = w.Write([]byte(`{"requeuedMissions":[{"id":"m1"}]}`))
	}))
	t.Cleanup(server.Close)
	return server, &posts
}

func TestOrchestratorReactor_NoRecoveryLeavesWithoutFenceConfirmation(t *testing.T) {
	server, posts := recoverServer(t)
	fencer := &fixedFencer{safe: false}
	var log []string
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reactor := &OrchestratorReactor{BaseURL: server.URL, Fencer: fencer, Context: ctx, RecoveryRetryPolicy: fastRetryPolicy(),
		Printf: func(format string, args ...any) { mu.Lock(); log = append(log, format); mu.Unlock() }}
	reactor.OnTransition(Node{Name: "svc", DispatchNode: "cell-a"}, StatusHealthy, StatusUnreachable, "timeout")
	time.Sleep(400 * time.Millisecond) // the background retries run and are all refused
	if posts.Load() != 0 {
		t.Fatalf("a recovery was requested %d time(s) without a fence confirmation", posts.Load())
	}
	if fencer.calls.Load() < 2 {
		t.Errorf("the fence should be asked again on every retry, asked %d time(s)", fencer.calls.Load())
	}
	if reactor.FenceRefusals() < 2 || reactor.FenceConfirmations() != 0 {
		t.Errorf("counters: refused=%d confirmed=%d", reactor.FenceRefusals(), reactor.FenceConfirmations())
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, line := range log {
		if strings.Contains(line, "NOT requested") {
			found = true
		}
	}
	if !found {
		t.Error("the refusal must be logged")
	}
}

func TestOrchestratorReactor_RecoveryGoesOnAfterFenceConfirmation(t *testing.T) {
	server, posts := recoverServer(t)
	fencer := &fixedFencer{safe: true}
	reactor := &OrchestratorReactor{BaseURL: server.URL, Fencer: fencer, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "svc", DispatchNode: "cell-a"}, StatusHealthy, StatusUnreachable, "timeout")
	if posts.Load() != 1 || reactor.FenceConfirmations() != 1 {
		t.Fatalf("posts=%d confirmed=%d, want 1 and 1", posts.Load(), reactor.FenceConfirmations())
	}
}

func TestOrchestratorReactor_WithoutAFencerNothingChanges(t *testing.T) {
	server, posts := recoverServer(t)
	reactor := &OrchestratorReactor{BaseURL: server.URL, Printf: func(string, ...any) {}}
	reactor.OnTransition(Node{Name: "svc"}, StatusHealthy, StatusUnreachable, "timeout")
	if posts.Load() != 1 {
		t.Fatalf("posts = %d, want 1", posts.Load())
	}
}

func TestOrchestratorReactor_TheFenceKnowsHowLongTheNodeHasBeenFailing(t *testing.T) {
	server, _ := recoverServer(t)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := now
	fencer := &fixedFencer{safe: true}
	reactor := &OrchestratorReactor{BaseURL: server.URL, Fencer: fencer, Printf: func(string, ...any) {}, Now: func() time.Time { return clock }}
	node := Node{Name: "svc", DispatchNode: "cell-a"}
	reactor.OnTransition(node, StatusHealthy, StatusUnreachable, "x")
	if fencer.seen.Load() != 0 {
		t.Errorf("failingFor at the first look = %d ms, want 0", fencer.seen.Load())
	}
	clock = now.Add(30 * time.Second)
	reactor.OnTransition(node, StatusUnreachable, StatusInvalid, "x") // still failing: the clock of the failure does not restart
	reactor.mu.Lock()
	reactor.lastByNode = nil // skip the cooldown so that a second request is made
	reactor.recent = nil
	reactor.mu.Unlock()
	reactor.OnTransition(node, StatusInvalid, StatusUnreachable, "x")
	if got := fencer.seen.Load(); got != 30000 {
		t.Errorf("failingFor after 30 s = %d ms, want 30000", got)
	}
	reactor.OnTransition(node, StatusUnreachable, StatusHealthy, "back")
	reactor.mu.Lock()
	_, still := reactor.failingSince["svc"]
	reactor.mu.Unlock()
	if still {
		t.Error("a node that is healthy again is not failing any more")
	}
}

// the status endpoint reads the counters through this interface
var _ FenceCounter = (*OrchestratorReactor)(nil)

func TestLoadFencingConfig_MandatoryUnlessTurnedOffOnPurpose(t *testing.T) {
	dir := t.TempDir()
	write := func(text string) string {
		p := filepath.Join(dir, "f.json")
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	c, err := LoadFencingConfig(write(`{"statusUrl": "http://127.0.0.1:1/x"}`))
	if err != nil || !c.Enabled {
		t.Fatalf("a file that does not mention enabled must keep fencing on: enabled=%v err=%v", c.Enabled, err)
	}
	if _, err := LoadFencingConfig(write(`{}`)); err == nil {
		t.Error("an empty file keeps fencing on and so needs a statusUrl")
	}
	c, err = LoadFencingConfig(write(`{"enabled": false, "disabledReason": "bench without robots"}`))
	if err != nil || c.Enabled || c.DisabledReason != "bench without robots" {
		t.Errorf("an explicit false turns it off with its reason: %+v %v", c, err)
	}
	if _, err := LoadFencingConfig(write(`{"enabled": false, "disabledReason": "a\nb"}`)); err == nil {
		t.Error("a reason with a line break must be refused")
	}
}

func TestMissingFencerAlwaysRefuses(t *testing.T) {
	v := MissingFencer{}.Fence(context.Background(), Node{Name: "n"}, "cell-a", time.Hour)
	if v.Safe || !strings.Contains(v.Reason, "mandatory") {
		t.Errorf("verdict = %+v", v)
	}
}

func TestOrchestratorReactor_WithoutAFencingFileNothingIsRequeued(t *testing.T) {
	var posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		w.WriteHeader(200)
	}))
	defer server.Close()
	reactor := &OrchestratorReactor{BaseURL: server.URL, Fencer: MissingFencer{}, Printf: func(string, ...any) {}}
	if got := reactor.attemptRecovery(context.Background(), Node{Name: "n"}, "cell-a"); got != recoveryTransient {
		t.Errorf("outcome = %v, want a refusal that is retried", got)
	}
	if posts.Load() != 0 || reactor.FenceRefusals() != 1 {
		t.Errorf("posts=%d refused=%d: nothing must leave", posts.Load(), reactor.FenceRefusals())
	}
}
