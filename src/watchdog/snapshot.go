// HYDRA-UMC-NODE-HEALING - watchdog package: snapshot.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The versioned snapshot of the health of a node: the document healing-snapshot/1.
//
// One snapshot is written every time a node changes state, to an append-only file
// with one JSON object per line. Each snapshot carries the hash of the one before it, so a snapshot removed or edited afterwards breaks
// the chain and VerifySnapshots says where. The sequence only grows, also across restarts (it is read back from the last line).
//
// The document has no date or clock reading: it says what the state is and in which order it came, not when. A document without a
// node identity, a sequence or a health state is refused, both when it is written and when the file is checked.
//
// The snapshot is opt-in (--snapshot-file PATH). A snapshot that cannot be written is reported and never stops the watchdog.
package watchdog

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// SnapshotSchema is the version of the document.
const SnapshotSchema = "healing-snapshot/1"

const (
	snapshotGenesis       = "0000000000000000000000000000000000000000000000000000000000000000"
	maxSnapshotActions    = 5
	maxSnapshotTextLength = 200
)

// Snapshot is the document healing-snapshot/1.
type Snapshot struct {
	SchemaVersion string `json:"schema_version"`
	NodeID        string `json:"node_id"`
	// Sequence grows by one with every snapshot of the file, whatever the node.
	Sequence int64 `json:"sequence"`
	// Components maps a component to its version (the watchdog itself, always).
	Components map[string]string `json:"components"`
	// ConfigHash is the SHA-256 of the registry file the watchdog was started with ("" when it cannot be read).
	ConfigHash string `json:"config_hash"`
	// Health is the state the watchdog holds for the node (HEALTHY, DEGRADED, UNHEALTHY, UNREACHABLE, INVALID, MISCONFIGURED, UNKNOWN).
	Health string `json:"health"`
	// LastActions are the last few things that happened to the node, oldest first.
	LastActions []string `json:"last_actions"`
	// MissionsHeld are the ids of the jobs the node holds according to the state of the queue (only when it could be read).
	MissionsHeld []string `json:"missions_held,omitempty"`
	PrevHash     string   `json:"prev_hash"`
	Hash         string   `json:"hash"`
}

// Validate says why the snapshot is not a healing-snapshot/1 (nil when it is).
func (s Snapshot) Validate() error {
	if s.SchemaVersion != SnapshotSchema {
		return fmt.Errorf("snapshot: the schema is %q, not %q", s.SchemaVersion, SnapshotSchema)
	}
	if strings.TrimSpace(s.NodeID) == "" {
		return errors.New("snapshot: no node identity")
	}
	if s.Sequence < 1 {
		return errors.New("snapshot: no sequence")
	}
	if strings.TrimSpace(s.Health) == "" {
		return errors.New("snapshot: no health state")
	}
	if len(s.Components) == 0 {
		return errors.New("snapshot: no component versions")
	}
	return nil
}

// hashOf is the hash of the snapshot with its own Hash left empty.
func (s Snapshot) hashOf() string {
	s.Hash = ""
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SnapshotChain appends snapshots to a file. Use NewSnapshotChain; the zero value is not usable.
type SnapshotChain struct {
	Path string

	mu       sync.Mutex
	sequence int64
	previous string
}

// NewSnapshotChain opens the chain, continuing the sequence and the hash of the last line of an existing file.
func NewSnapshotChain(path string) (*SnapshotChain, error) {
	if path == "" {
		return nil, errors.New("snapshot: the path is empty")
	}
	chain := &SnapshotChain{Path: path, previous: snapshotGenesis}
	last := lastLine(path)
	if len(last) > 0 {
		var snap Snapshot
		if err := json.Unmarshal(last, &snap); err != nil || snap.Hash == "" {
			return nil, fmt.Errorf("snapshot: the last line of %s is not a snapshot: refusing to continue a damaged chain", path)
		}
		chain.sequence, chain.previous = snap.Sequence, snap.Hash
	}
	return chain, nil
}

// Append completes the snapshot (schema, sequence, previous hash, hash), checks it and writes it as one line.
func (c *SnapshotChain) Append(s Snapshot) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.SchemaVersion = SnapshotSchema
	s.Sequence = c.sequence + 1
	s.PrevHash = c.previous
	if len(s.LastActions) > maxSnapshotActions {
		s.LastActions = s.LastActions[len(s.LastActions)-maxSnapshotActions:]
	}
	if s.LastActions == nil {
		s.LastActions = []string{}
	}
	if err := s.Validate(); err != nil {
		return Snapshot{}, err
	}
	s.Hash = s.hashOf()
	raw, err := json.Marshal(s)
	if err != nil {
		return Snapshot{}, err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return Snapshot{}, err
	}
	file, err := os.OpenFile(c.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return Snapshot{}, err
	}
	if err := file.Close(); err != nil {
		return Snapshot{}, err
	}
	c.sequence, c.previous = s.Sequence, s.Hash
	return s, nil
}

// VerifySnapshots checks a snapshot file: every line is a valid snapshot, the hashes chain from the genesis and the sequence grows by
// one. It returns how many snapshots it read, and an error naming the first line that does not fit.
func VerifySnapshots(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	previous := snapshotGenesis
	var sequence int64
	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		count++
		var snap Snapshot
		if err := json.Unmarshal(line, &snap); err != nil {
			return count - 1, fmt.Errorf("snapshot line %d is not JSON", count)
		}
		if err := snap.Validate(); err != nil {
			return count - 1, fmt.Errorf("snapshot line %d: %w", count, err)
		}
		if snap.PrevHash != previous {
			return count - 1, fmt.Errorf("snapshot line %d: the chain is broken (a line before it was removed or changed)", count)
		}
		if snap.Hash != snap.hashOf() {
			return count - 1, fmt.Errorf("snapshot line %d: its content does not match its hash", count)
		}
		if snap.Sequence != sequence+1 {
			return count - 1, fmt.Errorf("snapshot line %d: the sequence is %d, expected %d", count, snap.Sequence, sequence+1)
		}
		previous, sequence = snap.Hash, snap.Sequence
	}
	return count, scanner.Err()
}
