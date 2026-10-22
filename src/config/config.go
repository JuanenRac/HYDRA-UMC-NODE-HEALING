// HYDRA-UMC-NODE-HEALING - node registry config
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
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
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/JuanenRac/hydra-umc-node-healing/src/watchdog"
)

// validNodeName is the shape allowed for a node name and for a dispatch node
// name: both end up in log lines and in a URL path.
var validNodeName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// validHeaderName is an HTTP header name (a token); the ones below are the connection's own and are not the registry's to set.
var validHeaderName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

var forbiddenHeaders = map[string]bool{"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "upgrade": true}

const maxHeaders = 8

// checkHeaders refuses a header the probe must not send; the value is never put in the message, because it can be a secret.
func checkHeaders(headers map[string]string) error {
	if len(headers) > maxHeaders {
		return fmt.Errorf("more than %d headers", maxHeaders)
	}
	for name, value := range headers {
		if !validHeaderName.MatchString(name) {
			return fmt.Errorf("header name %q is not valid", name)
		}
		if forbiddenHeaders[strings.ToLower(name)] {
			return fmt.Errorf("header %q is set by the probe itself and cannot be given", name)
		}
		if len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("the value of header %q is too long or has a line break", name)
		}
	}
	return nil
}

type nodeEntry struct {
	Name      string `json:"name"`
	Address   string `json:"address"`
	HealthURL string `json:"healthUrl"`
	// DispatchNode is the name this node has in the Orchestrator's mission
	// dispatch; only an entry that has one is ever recovered automatically.
	DispatchNode string `json:"dispatchNode"`
	// Headers are sent with every HTTP health probe of the node (only with a healthUrl).
	Headers map[string]string `json:"headers"`
}

// LoadNodes reads a JSON array of {"name": "...", "address": "host:port"}
// entries from path. An entry may carry "healthUrl" (an http:// or https://
// URL answered with 2xx when the node is well) instead of "address": most
// services here speak HTTP only, and a node with a healthUrl is probed over
// HTTP instead of the gRPC HealthService. Returns a clear error (not a panic) on a missing or
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
		if !validNodeName.MatchString(e.Name) {
			return nil, fmt.Errorf("node registry %q: entry %d has an invalid \"name\" %q - use letters, digits, '.', '_', '-' and ':' (up to 128 characters)", path, i, e.Name)
		}
		if e.DispatchNode != "" && !validNodeName.MatchString(e.DispatchNode) {
			return nil, fmt.Errorf("node registry %q: entry %d (%s) has an invalid \"dispatchNode\" %q", path, i, e.Name, e.DispatchNode)
		}
		if e.Address != "" && e.HealthURL != "" {
			return nil, fmt.Errorf("node registry %q: entry %d (%s) has both an \"address\" and a \"healthUrl\" - choose one probe", path, i, e.Name)
		}
		if e.Address != "" {
			if host, port, aerr := net.SplitHostPort(e.Address); aerr != nil || host == "" || port == "" {
				return nil, fmt.Errorf("node registry %q: entry %d (%s) has an \"address\" that is not host:port: %q", path, i, e.Name, e.Address)
			}
		}
		if len(e.Headers) > 0 {
			if e.HealthURL == "" {
				return nil, fmt.Errorf("node registry %q: entry %d (%s) has \"headers\" but no \"healthUrl\": headers are only sent by the HTTP probe", path, i, e.Name)
			}
			if herr := checkHeaders(e.Headers); herr != nil {
				return nil, fmt.Errorf("node registry %q: entry %d (%s): %v", path, i, e.Name, herr)
			}
		}
		if e.Address == "" && e.HealthURL == "" {
			return nil, fmt.Errorf("node registry %q: entry %d (%s) has neither an \"address\" nor a \"healthUrl\"", path, i, e.Name)
		}
		if e.HealthURL != "" {
			u, perr := url.Parse(e.HealthURL)
			if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("node registry %q: entry %d (%s) has a \"healthUrl\" that is not an http:// or https:// URL: %q", path, i, e.Name, e.HealthURL)
			}
		}
		// Watchdog.state is keyed by Name alone (see watchdog.go), so two
		// entries sharing a name would silently overwrite each other's
		// state on every poll - fail loudly here instead.
		if first, dup := seen[e.Name]; dup {
			return nil, fmt.Errorf("node registry %q: entry %d (%s) duplicates the \"name\" of entry %d - names must be unique", path, i, e.Name, first)
		}
		seen[e.Name] = i
		nodes = append(nodes, watchdog.Node{Name: e.Name, Address: e.Address, HealthURL: e.HealthURL, DispatchNode: e.DispatchNode, Headers: e.Headers})
	}
	return nodes, nil
}
