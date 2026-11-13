// HYDRA-UMC-NODE-HEALING - config package: swarm_run_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The refresh loop of the swarm registry against a simulated SWARM-SYNC that changes, fails and recovers between reads.
package config

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type runLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *runLog) printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, format)
}

func (l *runLog) count(part string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

func waitForCount(t *testing.T, what string, limit time.Duration, value func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if value() >= want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (have %d, want %d)", what, value(), want)
}

func TestSwarmRun_OnlyAChangeOfTheAnnouncedNodesCallsBackAndAFailedReadChangesNothing(t *testing.T) {
	var state atomic.Value
	setState := func(entries map[string]string) {
		raw, _ := json.Marshal(stateOf(t, entries))
		state.Store(raw)
	}
	setState(map[string]string{"healing/node/a": announce("a", "http://127.0.0.1:9/x", "", "cell", false, testKey)})
	var status atomic.Int64
	server := swarmServer(t, &state, "", &status)
	logs := &runLog{}
	r := newRegistry(t, server.URL, nil)
	r.Printf = logs.printf
	r.Config.RefreshSeconds = 1 // below the validated minimum on purpose: the loop itself does not validate again

	var changes atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx, func() { changes.Add(1) }); close(done) }()

	waitForCount(t, "the first announced node", 6*time.Second, func() int { return int(changes.Load()) }, 1)
	time.Sleep(1300 * time.Millisecond) // one more read of the same state: no second call
	if got := changes.Load(); got != 1 {
		t.Fatalf("onChange called %d times for an unchanged state", got)
	}

	status.Store(500) // SWARM-SYNC fails: the nodes stay and the failure is logged
	waitForCount(t, "the failure line", 6*time.Second, func() int { return logs.count("stay as they are") }, 1)
	if got := len(r.Announced()); got != 1 {
		t.Fatalf("a failed read left %d nodes, want the 1 already known", got)
	}
	if got := changes.Load(); got != 1 {
		t.Fatalf("a failed read called onChange (%d)", got)
	}

	status.Store(0) // it recovers with a second node
	setState(map[string]string{
		"healing/node/a": announce("a", "http://127.0.0.1:9/x", "", "cell", false, testKey),
		"healing/node/b": announce("b", "http://127.0.0.1:9/y", "", "cell-b", false, testKey),
	})
	waitForCount(t, "the second change", 6*time.Second, func() int { return int(changes.Load()) }, 2)
	if got := len(r.Announced()); got != 2 {
		t.Fatalf("announced %d nodes, want 2", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop when its context ended")
	}
}

func TestSwarmRun_StopsAtOnceWhenTheContextIsAlreadyDone(t *testing.T) {
	var state atomic.Value
	state.Store([]byte(`{}`))
	var status atomic.Int64
	r := newRegistry(t, swarmServer(t, &state, "", &status).URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx, func() { t.Error("no read, no callback") }); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run ignored a finished context")
	}
}
