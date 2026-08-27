// HYDRA-UMC-NODE-HEALING - watchdog package tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Real gRPC round-trip tests: an in-process fake node implementing
// HealthService for real (net.Listen on 127.0.0.1:0, a real *grpc.Server),
// polled by a real Watchdog over a real (loopback) network connection -
// not a mocked client. Verifies the 3 behaviours the README's "HEALING
// WORKFLOW" diagram promises: detecting a healthy node, detecting an
// unreachable one, and detecting recovery - each exactly once per actual
// transition, not once per tick.
package watchdog

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	healthpb "github.com/JuanenRac/hydra-umc-node-healing/src/healthpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// fakeNode is a minimal real HealthService server whose reported state can
// be changed at runtime (simulating a node degrading/recovering) or shut
// down entirely (simulating a crash/network partition).
type fakeNode struct {
	healthpb.UnimplementedHealthServiceServer
	mu    sync.Mutex
	state healthpb.HealthState
	name  string
}

func (f *fakeNode) Check(ctx context.Context, _ *healthpb.Empty) (*healthpb.HealthReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &healthpb.HealthReport{
		Identity: &healthpb.NodeIdentity{Name: f.name},
		State:    f.state,
	}, nil
}

func (f *fakeNode) setState(s healthpb.HealthState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = s
}

// startFakeNode starts a real gRPC server on a real loopback port and
// returns its address plus a stop function.
func startFakeNode(t *testing.T, name string) (addr string, node *fakeNode, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	srv := grpc.NewServer()
	node = &fakeNode{name: name, state: healthpb.HealthState_HEALTH_STATE_OK}
	healthpb.RegisterHealthServiceServer(srv, node)
	go func() {
		_ = srv.Serve(lis) // returns non-nil on GracefulStop; test-only, ignored
	}()
	return lis.Addr().String(), node, func() {
		srv.GracefulStop()
	}
}

// recordingReactor captures every transition for assertions, in order.
type recordingReactor struct {
	mu          sync.Mutex
	transitions []string
}

func (r *recordingReactor) OnTransition(node Node, from, to Status, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transitions = append(r.transitions, node.Name+":"+from.String()+"->"+to.String())
}

func (r *recordingReactor) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.transitions))
	copy(out, r.transitions)
	return out
}

func TestWatchdog_DetectsHealthyOnFirstCheck(t *testing.T) {
	addr, _, stop := startFakeNode(t, "fake-vision-node")
	defer stop()

	reactor := &recordingReactor{}
	wd := NewWatchdog([]Node{{Name: "fake-vision-node", Address: addr}}, reactor)
	wd.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	wd.CheckTimeout = 2 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wd.pollAll(ctx)

	got := reactor.snapshot()
	want := []string{"fake-vision-node:UNKNOWN->HEALTHY"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}

func TestWatchdog_DetectsUnreachableThenRecovered(t *testing.T) {
	addr, _, stop := startFakeNode(t, "fake-cognitive-node")

	reactor := &recordingReactor{}
	wd := NewWatchdog([]Node{{Name: "fake-cognitive-node", Address: addr}}, reactor)
	wd.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	wd.CheckTimeout = 500 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Tick 1: node is up -> HEALTHY.
	wd.pollAll(ctx)

	// Simulate a crash: stop the server, same address now refuses
	// connections.
	stop()
	wd.pollAll(ctx)

	// Tick 3 while still down must NOT add another transition (no
	// per-tick spam for a node that is still in the same bad state).
	wd.pollAll(ctx)

	// Bring a fresh node back up on the SAME address to simulate recovery
	// (a real restart would bind the same host:port again).
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("could not rebind %s to simulate recovery: %v", addr, err)
	}
	srv2 := grpc.NewServer()
	healthpb.RegisterHealthServiceServer(srv2, &fakeNode{name: "fake-cognitive-node", state: healthpb.HealthState_HEALTH_STATE_OK})
	go func() { _ = srv2.Serve(lis) }()
	defer srv2.GracefulStop()

	wd.pollAll(ctx)

	got := reactor.snapshot()
	want := []string{
		"fake-cognitive-node:UNKNOWN->HEALTHY",
		"fake-cognitive-node:HEALTHY->UNREACHABLE",
		"fake-cognitive-node:UNREACHABLE->HEALTHY",
	}
	if len(got) != len(want) {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("transitions = %v, want %v", got, want)
		}
	}
}

func TestWatchdog_DegradedIsReportedAsDistinctFromHealthy(t *testing.T) {
	addr, node, stop := startFakeNode(t, "fake-orchestrator")
	defer stop()
	node.setState(healthpb.HealthState_HEALTH_STATE_DEGRADED)

	reactor := &recordingReactor{}
	wd := NewWatchdog([]Node{{Name: "fake-orchestrator", Address: addr}}, reactor)
	wd.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wd.pollAll(ctx)

	got := reactor.snapshot()
	if len(got) != 1 || got[0] != "fake-orchestrator:UNKNOWN->DEGRADED" {
		t.Fatalf("transitions = %v, want [fake-orchestrator:UNKNOWN->DEGRADED]", got)
	}
}
