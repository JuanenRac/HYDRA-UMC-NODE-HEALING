// HYDRA-UMC-NODE-HEALING - watchdog package: every node is polled on its own
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"context"
	"net"
	"testing"
	"time"
)

// startSilentNode listens and accepts connections but never answers a thing: a node whose process hangs.
func startSilentNode(t *testing.T) (addr string, stop func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				_ = c.Close()
			}
		}()
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
			select {
			case <-done:
				return
			default:
			}
		}
	}()
	return lis.Addr().String(), func() { close(done); _ = lis.Close() }
}

func checksOf(w *Watchdog, name string) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.stats[name]; s != nil {
		return s.checks
	}
	return 0
}

func TestRun_ANodeThatDoesNotAnswerDoesNotDelayTheOthers(t *testing.T) {
	okAddr, _, stopOK := startFakeNode(t, "ok")
	defer stopOK()
	silentAddr, stopSilent := startSilentNode(t)
	defer stopSilent()

	wd := NewWatchdog([]Node{{Name: "ok", Address: okAddr}, {Name: "silent", Address: silentAddr}}, nil)
	wd.Interval = 25 * time.Millisecond
	wd.CheckTimeout = 400 * time.Millisecond
	wd.RetryPolicy = RetryPolicy{MaxAttempts: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { wd.Run(ctx); close(done) }()

	// the silent node's first poll takes about 3 x 400 ms; in the first 600 ms the healthy node is checked again and again
	time.Sleep(600 * time.Millisecond)
	if got := checksOf(wd, "ok"); got < 8 {
		t.Fatalf("the healthy node was checked %d times in 600 ms with a silent node next to it, want at least 8 (its ticks must not wait for the silent one)", got)
	}
	if got := checksOf(wd, "silent"); got != 0 {
		t.Fatalf("the silent node finished %d polls in 600 ms; its first poll should still be running", got)
	}
	// and its poll is not queued behind itself: it is still the one first poll, marked as running (the healthy node may
	// also be mid-poll at the instant of looking, so the size of the set says nothing; TestStartDue covers the skipping)
	wd.mu.Lock()
	silentRunning := wd.polling["silent"]
	wd.mu.Unlock()
	if !silentRunning {
		t.Fatal("the silent node's first poll should still be marked as running")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after its context was cancelled while a node was still being polled")
	}
	wd.mu.Lock()
	left := len(wd.polling)
	wd.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d polls still marked as running after Run returned", left)
	}
}

func TestPollAll_StillWaitsForEveryNode(t *testing.T) {
	addr, _, stop := startFakeNode(t, "svc")
	defer stop()
	wd := NewWatchdog([]Node{{Name: "svc", Address: addr}}, nil)
	wd.pollAll(context.Background())
	if got := checksOf(wd, "svc"); got != 1 {
		t.Fatalf("checks = %d after pollAll, want 1 (pollAll waits for the polls it starts)", got)
	}
}

func TestStartDue_ANodeStillBeingPolledIsSkippedNotQueued(t *testing.T) {
	silentAddr, stopSilent := startSilentNode(t)
	defer stopSilent()
	wd := NewWatchdog([]Node{{Name: "silent", Address: silentAddr}}, nil)
	wd.CheckTimeout = 300 * time.Millisecond
	wd.RetryPolicy = RetryPolicy{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	ctx := context.Background()

	first := wd.startDue(ctx)
	for i := 0; i < 5; i++ {
		wd.startDue(ctx) // ticks while the first poll is running
	}
	first.Wait()
	wd.active.Wait()
	if got := checksOf(wd, "silent"); got != 1 {
		t.Fatalf("checks = %d, want 1: the ticks during a running poll must be skipped", got)
	}
	// once it has finished, the next tick polls again
	wd.startDue(ctx).Wait()
	if got := checksOf(wd, "silent"); got != 2 {
		t.Fatalf("checks = %d after the next tick, want 2", got)
	}
}
