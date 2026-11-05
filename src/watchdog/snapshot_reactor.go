// HYDRA-UMC-NODE-HEALING - watchdog package: snapshot_reactor.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/redact"
)

// SnapshotReactor writes a healing-snapshot/1 to the chain for every transition of a node. It decides nothing and sends nothing: it only
// records, and a snapshot that cannot be written is reported and dropped, never retried in a way that would delay the polling.
type SnapshotReactor struct {
	Chain *SnapshotChain
	// Version is the version of the watchdog, written as the component "node-healing".
	Version string
	// ConfigHash is the SHA-256 of the registry file (see HashFile).
	ConfigHash string
	// Missions, when set, is asked for the state of the queue so the snapshot names the jobs a node holds. A state that cannot be read
	// leaves the field out; the snapshot is written all the same.
	Missions MissionSource
	// MissionTimeout bounds one read of the state (3 seconds when zero).
	MissionTimeout time.Duration
	Context        context.Context
	Printf         func(format string, args ...any)

	mu      sync.Mutex
	actions map[string][]string
}

// HashFile is the SHA-256 of a file in hex, or "" when it cannot be read.
func HashFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func clipText(text string) string {
	if utf8.RuneCountInString(text) <= maxSnapshotTextLength {
		return text
	}
	return string([]rune(text)[:maxSnapshotTextLength])
}

// OnTransition implements Reactor.
func (r *SnapshotReactor) OnTransition(node Node, from, to Status, detail string) {
	printf := r.Printf
	if printf == nil {
		printf = func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	}
	action := clipText(redact.Secrets(fmt.Sprintf("%s -> %s: %s", from, to, detail)))
	r.mu.Lock()
	if r.actions == nil {
		r.actions = map[string][]string{}
	}
	history := append(r.actions[node.Name], action)
	if len(history) > maxSnapshotActions {
		history = history[len(history)-maxSnapshotActions:]
	}
	r.actions[node.Name] = history
	last := append([]string(nil), history...)
	r.mu.Unlock()

	snap := Snapshot{
		NodeID:      node.Name,
		Components:  map[string]string{"node-healing": r.Version},
		ConfigHash:  r.ConfigHash,
		Health:      to.String(),
		LastActions: last,
	}
	if r.Missions != nil && node.DispatchNode != "" {
		ctx := r.Context
		if ctx == nil {
			ctx = context.Background()
		}
		timeout := r.MissionTimeout
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		readCtx, cancel := context.WithTimeout(ctx, timeout)
		state, err := r.Missions.MissionState(readCtx)
		cancel()
		if err != nil {
			printf("[node-healing] snapshot: the state of the missions could not be read: %s", redact.Secrets(err.Error()))
		} else {
			snap.MissionsHeld = state.Held(node.DispatchNode)
		}
	}
	if _, err := r.Chain.Append(snap); err != nil {
		printf("[node-healing] snapshot: not written: %s", redact.Secrets(err.Error()))
	}
}
