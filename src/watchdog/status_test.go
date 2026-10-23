// HYDRA-UMC-NODE-HEALING - watchdog status endpoint, reload and kept connections: tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	healthpb "github.com/JuanenRac/hydra-umc-node-healing/src/healthpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type quietReactor struct{}

func (quietReactor) OnTransition(Node, Status, Status, string) {}

type countingReactor struct{ n atomic.Int64 }

func (c *countingReactor) OnTransition(Node, Status, Status, string) { c.n.Add(1) }
func (c *countingReactor) RecoveryRequests() int64                  { return 42 }

func httpNodeServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	t.Cleanup(server.Close)
	return server
}

func checkedWatchdog(t *testing.T, nodes []Node) *Watchdog {
	t.Helper()
	wd := NewWatchdog(nodes, quietReactor{})
	wd.CheckTimeout = time.Second
	wd.RetryPolicy = RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	for _, n := range nodes {
		wd.pollOne(context.Background(), n)
	}
	return wd
}

func TestSnapshotReportsStatusChecksAndTransitions(t *testing.T) {
	up, down := httpNodeServer(t, 200), httpNodeServer(t, 503)
	wd := checkedWatchdog(t, []Node{{Name: "b-down", HealthURL: down.URL}, {Name: "a-up", HealthURL: up.URL}})
	wd.pollOne(context.Background(), wd.Nodes[1])
	snaps := wd.Snapshot()
	if len(snaps) != 2 || snaps[0].Name != "a-up" || snaps[1].Name != "b-down" {
		t.Fatalf("snapshots %+v, want sorted by name", snaps)
	}
	if snaps[0].Status != StatusHealthy || snaps[0].Checks != 2 || snaps[0].Transitions != 1 {
		t.Fatalf("a-up = %+v, want healthy, 2 checks, 1 transition (the second check changed nothing)", snaps[0])
	}
	if snaps[1].Status == StatusHealthy || snaps[1].LastCheck.IsZero() {
		t.Fatalf("b-down = %+v", snaps[1])
	}
}

func TestSnapshotOfANodeNotCheckedYetIsUnknown(t *testing.T) {
	wd := NewWatchdog([]Node{{Name: "new", HealthURL: "http://127.0.0.1:1"}}, quietReactor{})
	snaps := wd.Snapshot()
	if len(snaps) != 1 || snaps[0].Status != StatusUnknown || snaps[0].Checks != 0 {
		t.Fatalf("snapshots %+v", snaps)
	}
}

func get(t *testing.T, handler http.Handler, path, token string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	body, _ := io.ReadAll(recorder.Result().Body)
	return recorder.Code, string(body)
}

func TestHealthNeedsNoTokenAndNamesNoNode(t *testing.T) {
	wd := checkedWatchdog(t, []Node{{Name: "secret-name", HealthURL: httpNodeServer(t, 200).URL}})
	server := &StatusServer{Watchdog: wd, Version: "9.9.9", Started: time.Now().Add(-90 * time.Second), Token: "tok"}
	code, body := get(t, server.Handler(), "/health", "")
	if code != 200 || !strings.Contains(body, `"status":"ok"`) || !strings.Contains(body, `"version":"9.9.9"`) || !strings.Contains(body, `"nodes":1`) {
		t.Fatalf("/health = %d %s", code, body)
	}
	if strings.Contains(body, "secret-name") {
		t.Fatalf("/health names a node: %s", body)
	}
}

func TestMetricsAreInPrometheusTextAndWantTheTokenWhenOneIsSet(t *testing.T) {
	wd := checkedWatchdog(t, []Node{{Name: "up", HealthURL: httpNodeServer(t, 200).URL}, {Name: "bad\"name", HealthURL: httpNodeServer(t, 503).URL}})
	server := &StatusServer{Watchdog: wd, Version: "1.2.3", Started: time.Now(), Token: "tok", Recoveries: &countingReactor{}}
	if code, _ := get(t, server.Handler(), "/metrics", ""); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", code)
	}
	if code, _ := get(t, server.Handler(), "/metrics", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d, want 401", code)
	}
	code, body := get(t, server.Handler(), "/metrics", "tok")
	if code != 200 {
		t.Fatalf("with the token: %d", code)
	}
	for _, want := range []string{
		`hydra_node_healing_info{version="1.2.3"} 1`,
		"hydra_node_healing_nodes 2",
		`hydra_node_healing_node_status{node="up",status="HEALTHY"} 1`,
		`hydra_node_healing_node_checks_total{node="up"} 1`,
		`hydra_node_healing_node_transitions_total{node="up"} 1`,
		`hydra_node_healing_node_status{node="bad\"name"`, // a quote in a name cannot break the line
		"hydra_node_healing_recovery_requests_total 42",
		"# TYPE hydra_node_healing_node_status gauge",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics lack %q:\n%s", want, body)
		}
	}
}

func TestMetricsWithoutATokenAreOpenOnLoopback(t *testing.T) {
	wd := checkedWatchdog(t, nil)
	server := &StatusServer{Watchdog: wd, Version: "1", Started: time.Now()}
	if code, _ := get(t, server.Handler(), "/metrics", ""); code != 200 {
		t.Fatalf("code %d", code)
	}
	if code, _ := get(t, server.Handler(), "/nothing", ""); code != 404 {
		t.Fatalf("unknown route: %d", code)
	}
	request := httptest.NewRequest(http.MethodPost, "/health", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health: %d", recorder.Code)
	}
}

func TestListenAddressMustBeLoopbackUnlessATokenProtectsIt(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9100", "[::1]:9100", "localhost:9100"} {
		if err := ValidateListen(address, ""); err != nil {
			t.Fatalf("%s should be allowed: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:9100", ":9100", "192.168.0.5:9100"} {
		if err := ValidateListen(address, ""); err == nil {
			t.Fatalf("%s should need a token", address)
		}
		if err := ValidateListen(address, "tok"); err != nil {
			t.Fatalf("%s with a token: %v", address, err)
		}
	}
	if err := ValidateListen("no-port", "tok"); err == nil {
		t.Fatal("an address without a port is refused")
	}
}

func TestServeAnswersAndStopsWithItsContext(t *testing.T) {
	wd := checkedWatchdog(t, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	server := &StatusServer{Watchdog: wd, Version: "1", Started: time.Now()}
	go func() { done <- server.Serve(ctx, address) }()
	var response *http.Response
	for i := 0; i < 50; i++ {
		response, err = http.Get("http://" + address + "/health")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("Serve returned %v", serveErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop with its context")
	}
}

func TestSetNodesKeepsTheStateOfNodesThatStayAndForgetsTheOthers(t *testing.T) {
	a, b := httpNodeServer(t, 200), httpNodeServer(t, 200)
	wd := checkedWatchdog(t, []Node{{Name: "a", HealthURL: a.URL}, {Name: "b", HealthURL: b.URL}})
	wd.SetNodes([]Node{{Name: "b", HealthURL: b.URL}, {Name: "c", HealthURL: a.URL}})
	snaps := wd.Snapshot()
	if len(snaps) != 2 || snaps[0].Name != "b" || snaps[1].Name != "c" {
		t.Fatalf("after reload: %+v", snaps)
	}
	if snaps[0].Status != StatusHealthy || snaps[0].Checks != 1 {
		t.Fatalf("b lost its state: %+v", snaps[0])
	}
	if snaps[1].Status != StatusUnknown {
		t.Fatalf("c is new: %+v", snaps[1])
	}
}

// ---- kept gRPC connections ----

type acceptCounter struct {
	net.Listener
	accepted atomic.Int64
}

func (l *acceptCounter) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

type namedNode struct {
	healthpb.UnimplementedHealthServiceServer
	name string
}

func (n namedNode) Check(context.Context, *healthpb.Empty) (*healthpb.HealthReport, error) {
	return &healthpb.HealthReport{Identity: &healthpb.NodeIdentity{Name: n.name}, State: healthpb.HealthState_HEALTH_STATE_OK}, nil
}

func startCountedNode(t *testing.T, name string) (string, *acceptCounter, func()) {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	counted := &acceptCounter{Listener: inner}
	server := grpc.NewServer()
	healthpb.RegisterHealthServiceServer(server, namedNode{name: name})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = server.Serve(counted) }()
	return inner.Addr().String(), counted, func() { server.Stop(); wg.Wait() }
}

func TestAHealthyNodeIsPolledOverOneKeptConnection(t *testing.T) {
	address, counted, stop := startCountedNode(t, "svc")
	defer stop()
	wd := NewWatchdog([]Node{{Name: "svc", Address: address}}, quietReactor{})
	wd.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	defer wd.Close()
	for i := 0; i < 5; i++ {
		if status, detail := wd.CheckOnce(context.Background(), wd.Nodes[0]); status != StatusHealthy {
			t.Fatalf("poll %d: %s (%s)", i, status, detail)
		}
	}
	if got := counted.accepted.Load(); got != 1 {
		t.Fatalf("the node accepted %d connections for 5 polls, want 1", got)
	}
}

func TestANodeThatGoesAwayIsSeenAndSeenAgainOnItsReturn(t *testing.T) {
	address, _, stop := startCountedNode(t, "svc")
	wd := NewWatchdog([]Node{{Name: "svc", Address: address}}, quietReactor{})
	wd.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	wd.CheckTimeout = 500 * time.Millisecond
	wd.RetryPolicy = RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	defer wd.Close()
	if status, _ := wd.CheckOnce(context.Background(), wd.Nodes[0]); status != StatusHealthy {
		t.Fatalf("first: %s", status)
	}
	stop()
	if status, _ := wd.CheckOnce(context.Background(), wd.Nodes[0]); status != StatusUnreachable {
		t.Fatalf("after the node stopped: %s", status)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Skipf("the port was taken meanwhile: %v", err)
	}
	server := grpc.NewServer()
	healthpb.RegisterHealthServiceServer(server, namedNode{name: "svc"})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, detail := wd.CheckOnce(context.Background(), wd.Nodes[0])
		if status == StatusHealthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node came back but the watchdog still says %s (%s)", status, detail)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRemovingANodeFromTheRegistryClosesItsConnection(t *testing.T) {
	address, counted, stop := startCountedNode(t, "svc")
	defer stop()
	wd := NewWatchdog([]Node{{Name: "svc", Address: address}}, quietReactor{})
	wd.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	defer wd.Close()
	wd.CheckOnce(context.Background(), wd.Nodes[0])
	wd.connMu.Lock()
	kept := len(wd.conns)
	wd.connMu.Unlock()
	wd.SetNodes(nil)
	wd.connMu.Lock()
	after := len(wd.conns)
	wd.connMu.Unlock()
	if kept != 1 || after != 0 || counted.accepted.Load() != 1 {
		t.Fatalf("connections kept %d, after the reload %d", kept, after)
	}
}
