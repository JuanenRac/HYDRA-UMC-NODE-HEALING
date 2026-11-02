// HYDRA-UMC-NODE-HEALING - watchdog package: watchstream.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Detection without waiting for the next poll. A node that serves the server-streaming Watch() RPC of the health service is
// asked once for a stream and every report it sends is taken as it arrives, so a change of state is seen when the node says it
// and not up to one interval later. It is off unless the registry entry says "watch": true (and has an "address").
//
// The poll stays as the safety net. While a stream has reported inside WatchFreshFor the poll of that node is skipped; when it
// has been quiet longer, or it broke, or the node does not serve Watch() at all, the next tick polls Check() as before. A stream
// that breaks is followed at once by a poll, so a crash is classified UNREACHABLE without waiting for the tick.
//
// The method is not in the vendored contract yet: hydra_common.proto only mentions it as a later addition. The client uses the
// generic stream interface of grpc with the messages that exist (Empty in, HealthReport out), so no generated code changes; the day
// the contract declares `rpc Watch(Empty) returns (stream HealthReport)` this is the method it will be.
package watchdog

import (
	"context"
	"errors"
	"io"
	"time"

	healthpb "github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/healthpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WatchFullMethodName is the method a node serves for the stream.
const WatchFullMethodName = "/hydra.common.v1.HealthService/Watch"

// DefaultWatchFreshFor is how long after a report of its stream the poll of a node is skipped.
const DefaultWatchFreshFor = 20 * time.Second

const (
	watchBackoffStart       = time.Second
	watchBackoffMax         = 30 * time.Second
	watchBackoffUnsupported = 5 * time.Minute
)

func (w *Watchdog) watchFreshFor() time.Duration {
	if w.WatchFreshFor > 0 {
		return w.WatchFreshFor
	}
	return DefaultWatchFreshFor
}

// watchFresh says whether the stream of the node reported recently enough for the poll to be skipped.
func (w *Watchdog) watchFresh(n Node) bool {
	if !n.Watch {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	last, ok := w.lastWatch[n.Name]
	return ok && time.Since(last) < w.watchFreshFor()
}

func (w *Watchdog) stillWatches(name string) bool {
	for _, n := range w.currentNodes() {
		if n.Name == name {
			return n.Watch && n.Address != "" && n.HealthURL == ""
		}
	}
	return false
}

// startWatch starts the stream loop of a node that asks for one and has none running.
func (w *Watchdog) startWatch(ctx context.Context, n Node) {
	if !n.Watch || n.Address == "" || n.HealthURL != "" {
		return
	}
	w.mu.Lock()
	if w.watching == nil {
		w.watching = make(map[string]bool)
	}
	if w.lastWatch == nil {
		w.lastWatch = make(map[string]time.Time)
	}
	running := w.watching[n.Name]
	if !running {
		w.watching[n.Name] = true
	}
	w.mu.Unlock()
	if running {
		return
	}
	w.active.Add(1)
	go func() {
		defer w.active.Done()
		defer func() {
			w.mu.Lock()
			delete(w.watching, n.Name)
			delete(w.lastWatch, n.Name)
			w.mu.Unlock()
		}()
		w.watchLoop(ctx, n)
	}()
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// watchLoop keeps a stream open to the node until the context ends or the node leaves the registry (or stops asking for one).
func (w *Watchdog) watchLoop(ctx context.Context, n Node) {
	backoff := watchBackoffStart
	for ctx.Err() == nil && w.stillWatches(n.Name) {
		reports, err := w.watchOnce(ctx, n)
		w.mu.Lock()
		delete(w.lastWatch, n.Name)
		w.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if reports > 0 {
			backoff = watchBackoffStart
			w.pollAfterBreak(ctx, n)
		}
		if status.Code(err) == codes.Unimplemented {
			backoff = watchBackoffUnsupported // the node does not serve Watch(): the poll does the work, and we look again rarely
		}
		if !sleepOrDone(ctx, backoff) {
			return
		}
		if backoff < watchBackoffMax {
			backoff *= 2
			if backoff > watchBackoffMax {
				backoff = watchBackoffMax
			}
		}
	}
}

// pollAfterBreak classifies the node with one ordinary check right after its stream ended, unless a poll of it is running.
func (w *Watchdog) pollAfterBreak(ctx context.Context, n Node) {
	w.mu.Lock()
	if w.polling == nil {
		w.polling = make(map[string]bool)
	}
	busy := w.polling[n.Name]
	if !busy {
		w.polling[n.Name] = true
	}
	w.mu.Unlock()
	if busy {
		return
	}
	defer func() {
		w.mu.Lock()
		delete(w.polling, n.Name)
		w.mu.Unlock()
	}()
	w.pollOne(ctx, n)
}

// watchOnce opens one stream and takes its reports until it ends. It returns how many reports arrived and why it ended.
func (w *Watchdog) watchOnce(ctx context.Context, n Node) (int, error) {
	conn, err := w.connection(n.Address)
	if err != nil {
		return 0, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := conn.NewStream(streamCtx, &grpc.StreamDesc{StreamName: "Watch", ServerStreams: true}, WatchFullMethodName)
	if err != nil {
		return 0, err
	}
	if err := stream.SendMsg(&healthpb.Empty{}); err != nil {
		return 0, err
	}
	if err := stream.CloseSend(); err != nil {
		return 0, err
	}
	reports := 0
	for {
		started := time.Now()
		report := &healthpb.HealthReport{}
		if err := stream.RecvMsg(report); err != nil {
			if errors.Is(err, io.EOF) {
				return reports, nil
			}
			return reports, err
		}
		reports++
		state, detail, _ := classifyReport(n, report)
		w.mu.Lock()
		if w.lastWatch == nil {
			w.lastWatch = make(map[string]time.Time)
		}
		w.lastWatch[n.Name] = time.Now()
		w.mu.Unlock()
		w.observe(n, state, detail, started, time.Since(started))
	}
}
