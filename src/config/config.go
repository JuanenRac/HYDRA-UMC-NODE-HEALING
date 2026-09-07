// HYDRA-UMC-NODE-HEALING - node registry config
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Static JSON registry of the nodes to watch. Deliberately not fetched
// from HYDRA-UMC-SWARM-SYNC - see the "static and not discovered
// dynamically" comment on watchdog.Node for why. Kept in its own package
// (not inlined in main.go) so a future SWARM-SYNC-backed loader can
// implement the same LoadNodes signature without touching main.go.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/JuanenRac/hydra-umc-node-healing/src/watchdog"
)

type nodeEntry struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// LoadNodes reads a JSON array of {"name": "...", "address": "host:port"}
// entries from path. Returns a clear error (not a panic) on a missing or
// malformed file, since a bad config here means the watchdog would
// silently watch nothing - worth failing loudly on startup instead.
func LoadNodes(path string) ([]watchdog.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading node registry %q: %w", path, err)
	}
	var entries []nodeEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parsing node registry %q: %w", path, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("node registry %q is empty - nothing to watch", path)
	}
	nodes := make([]watchdog.Node, 0, len(entries))
	seen := make(map[string]int, len(entries))
	for i, e := range entries {
		if e.Name == "" {
			return nil, fmt.Errorf("node registry %q: entry %d has no \"name\"", path, i)
		}
		if e.Address == "" {
			return nil, fmt.Errorf("node registry %q: entry %d (%s) has no \"address\"", path, i, e.Name)
		}
		// Watchdog.state is keyed by Name alone (see watchdog.go), so two
		// entries sharing a name would silently overwrite each other's
		// state on every poll - fail loudly here instead.
		if first, dup := seen[e.Name]; dup {
			return nil, fmt.Errorf("node registry %q: entry %d (%s) duplicates the \"name\" of entry %d - names must be unique", path, i, e.Name, first)
		}
		seen[e.Name] = i
		nodes = append(nodes, watchdog.Node{Name: e.Name, Address: e.Address})
	}
	return nodes, nil
}
