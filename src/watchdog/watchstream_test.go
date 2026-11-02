// HYDRA-UMC-NODE-HEALING - watchdog package: watchstream_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// A real gRPC server that serves the Watch() stream (declared here by hand, because the vendored contract does not carry it yet) and a
// real Watchdog that follows it over a loopback connection.
package watchdog

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	healthpb "github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/healthpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// watchNode serves Check() and Watch(). Reports pushed on updates are streamed to every open Watch().
type watchNode struct {
	name    string
	checks  atomic.Int64
	updates chan healthpb.HealthState
}

type watchService interface{}

func (n *watchNode) desc() *grpc.ServiceDesc {
	return &grpc.ServiceDesc{
		ServiceName: "hydra.common.v1.HealthService",
		HandlerType: (*watchService)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Check",
			Handler: func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				if err := dec(&healthpb.Empty{}); err != nil {
					return nil, err
				}
				n.checks.Add(1)
				return &healthpb.HealthReport{Identity: &healthpb.NodeIdentity{Name: n.name}, State: healthpb.HealthState_HEALTH_STATE_OK}, nil
			},
		}},
		Streams: []grpc.StreamDesc{{
			StreamName:    "Watch",
			ServerStreams: true,
			Handler: func(_ any, stream grpc.ServerStream) error {
				if err := stream.RecvMsg(&healthpb.Empty{}); err != nil {
					return err
				}
				for {
					select {
					case <-stream.Context().Done():
						return nil
					case state := <-n.updates:
						if err := stream.SendMsg(&healthpb.HealthReport{Identity: &healthpb.NodeIdentity{Name: n.name}, State: state, Detail: "streamed"}); err != nil {
							return err
						}
					}
				}
			},
		}},
	}
}

func startWatchNode(t *testing.T, name string) (string, *watchNode, *grpc.Server) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	node := &watchNode{name: name, updates: make(chan healthpb.HealthState, 8)}
	srv := grpc.NewServer()
	srv.RegisterService(node.desc(), node)
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), node, srv
}

// waitFor polls a condition for up to the given time.
func waitUntil(t *testing.T, limit time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func newWatchdog(nodes []Node, reactor Reactor) *Watchdog {
	wd := NewWatchdog(nodes, reactor)
	wd.DialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	wd.Interval = time.Hour // the tick never comes: whatever is seen is seen by the stream or by the poll that follows a break
	wd.CheckTimeout = 3 * time.Second
	wd.RetryPolicy = RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	return wd
}

func TestWatch_AReportIsSeenWithoutWaitingForAPoll(t *testing.T) {
	addr, node, srv := startWatchNode(t, "watched")
	defer srv.Stop()
	reactor := &recordingReactor{}
	wd := newWatchdog([]Node{{Name: "watched", Address: addr, Watch: true}}, reactor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	waitUntil(t, 3*time.Second, "the first state", func() bool { return contains(reactor.snapshot(), "watched:UNKNOWN->HEALTHY") })
	node.updates <- healthpb.HealthState_HEALTH_STATE_UNHEALTHY
	waitUntil(t, 3*time.Second, "the streamed UNHEALTHY", func() bool { return contains(reactor.snapshot(), "watched:HEALTHY->UNHEALTHY") })
	node.updates <- healthpb.HealthState_HEALTH_STATE_OK
	waitUntil(t, 3*time.Second, "the streamed recovery", func() bool { return contains(reactor.snapshot(), "watched:UNHEALTHY->HEALTHY") })
	if checks := node.checks.Load(); checks > 1 {
		t.Errorf("the interval is an hour, so only the first look may poll; Check() was called %d times", checks)
	}
}

func TestWatch_ABrokenStreamIsClassifiedAtOnce(t *testing.T) {
	addr, node, srv := startWatchNode(t, "crashing")
	reactor := &recordingReactor{}
	wd := newWatchdog([]Node{{Name: "crashing", Address: addr, Watch: true}}, reactor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	node.updates <- healthpb.HealthState_HEALTH_STATE_OK
	waitUntil(t, 3*time.Second, "the first state", func() bool { return len(reactor.snapshot()) >= 1 })
	srv.Stop() // the node crashes: the stream breaks, and the interval is an hour
	waitUntil(t, 5*time.Second, "UNREACHABLE", func() bool { return contains(reactor.snapshot(), "crashing:HEALTHY->UNREACHABLE") })
}

func TestWatch_ANodeWithoutTheStreamFallsBackToThePoll(t *testing.T) {
	addr, _, stop := startFakeNode(t, "old-node") // serves Check() only: Watch() is Unimplemented
	defer stop()
	reactor := &recordingReactor{}
	wd := newWatchdog([]Node{{Name: "old-node", Address: addr, Watch: true}}, reactor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, 3*time.Second, "the poll", func() bool { return contains(reactor.snapshot(), "old-node:UNKNOWN->HEALTHY") })
}

func TestWatch_TheStreamIsNotUsedUnlessAskedFor(t *testing.T) {
	addr, node, srv := startWatchNode(t, "plain")
	defer srv.Stop()
	reactor := &recordingReactor{}
	wd := newWatchdog([]Node{{Name: "plain", Address: addr}}, reactor)
	wd.pollAll(context.Background())
	node.updates <- healthpb.HealthState_HEALTH_STATE_UNHEALTHY
	time.Sleep(200 * time.Millisecond)
	if got := reactor.snapshot(); len(got) != 1 || got[0] != "plain:UNKNOWN->HEALTHY" {
		t.Fatalf("a node that did not ask for the stream must only be polled, got %v", got)
	}
	wd.mu.Lock()
	defer wd.mu.Unlock()
	if len(wd.watching) != 0 {
		t.Error("no stream should have been started")
	}
}

func TestWatch_AFreshStreamSkipsThePoll(t *testing.T) {
	wd := NewWatchdog(nil, nil)
	n := Node{Name: "x", Address: "127.0.0.1:1", Watch: true}
	if wd.watchFresh(n) {
		t.Error("no report yet: not fresh")
	}
	wd.mu.Lock()
	wd.lastWatch = map[string]time.Time{"x": time.Now()}
	wd.mu.Unlock()
	if !wd.watchFresh(n) {
		t.Error("a report just arrived: fresh")
	}
	if wd.watchFresh(Node{Name: "x", Address: "127.0.0.1:1"}) {
		t.Error("a node that did not ask for the stream is never fresh")
	}
	wd.WatchFreshFor = time.Millisecond
	time.Sleep(5 * time.Millisecond)
	if wd.watchFresh(n) {
		t.Error("an old report is not fresh")
	}
}

func TestWatch_AStreamStopsWhenTheNodeLeavesTheRegistry(t *testing.T) {
	addr, node, srv := startWatchNode(t, "leaving")
	defer srv.Stop()
	reactor := &recordingReactor{}
	wd := newWatchdog([]Node{{Name: "leaving", Address: addr, Watch: true}}, reactor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	node.updates <- healthpb.HealthState_HEALTH_STATE_OK
	waitUntil(t, 3*time.Second, "the first state", func() bool { return len(reactor.snapshot()) >= 1 })
	wd.SetNodes(nil)
	if wd.stillWatches("leaving") {
		t.Error("a node that left the registry must not be watched any more")
	}
}
