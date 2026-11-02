// HYDRA-UMC-NODE-HEALING - node registry from SWARM-SYNC
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The static registry can be completed with the nodes the swarm itself announces. SWARM-SYNC serves the state it reconciles as a
// plain {"key": "value"} object on GET /state; a node announces itself with a key under a prefix (healing/node/<name> by default)
// whose value is a small JSON object with the health route, the dispatch node and a signature. The signature is an HMAC-SHA256 of
// the fields with a key that only the operator and the announcing nodes hold, so a node that can write to the shared state but does
// not hold the key cannot make the watchdog look at (or recover) a node of its choosing. An entry that does not verify, or that
// does not pass the same checks as a line of the static file, is left out and counted; one bad entry never spoils the rest.
//
// It is off unless a file is given with -swarm-config, and the static registry stays the base: a static entry wins over an
// announced one with the same name. When SWARM-SYNC cannot be read the nodes already known stay as they were.
package config

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/watchdog"
)

const (
	maxSwarmBody       = 1 << 20
	defaultKeyPrefix   = "healing/node/"
	defaultRefreshSecs = 30
)

// SwarmConfig is the file given with -swarm-config.
type SwarmConfig struct {
	// Enabled turns the dynamic registry on.
	Enabled bool `json:"enabled"`
	// URL is the base of the SWARM-SYNC API, for example http://127.0.0.1:8112.
	URL string `json:"url"`
	// TokenFile holds the shared secret of SWARM-SYNC, sent as a bearer token ("" when SWARM-SYNC has none).
	TokenFile string `json:"tokenFile"`
	// KeyPrefix is the start of the keys that announce a node.
	KeyPrefix string `json:"keyPrefix"`
	// SigningKeyFile holds the key of the HMAC that signs an announcement.
	SigningKeyFile string `json:"signingKeyFile"`
	// RequireSignature, true unless the file says otherwise, leaves out an announcement whose signature does not verify.
	RequireSignature *bool `json:"requireSignature"`
	// RefreshSeconds is how often SWARM-SYNC is read (5 to 3600).
	RefreshSeconds int `json:"refreshSeconds"`
	// TimeoutSeconds bounds a read (1 to 60, 5 by default).
	TimeoutSeconds int `json:"timeoutSeconds"`
	// MaxEntries is the most announcements taken in one read (1 to 1024, 256 by default).
	MaxEntries int `json:"maxEntries"`
	// AllowPlaintext lets a SWARM-SYNC that is not on this machine be read over http.
	AllowPlaintext bool `json:"allowPlaintext"`
}

func (c SwarmConfig) signatureRequired() bool { return c.RequireSignature == nil || *c.RequireSignature }

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return host == "::1" || strings.HasPrefix(host, "127.")
}

// Validate refuses a configuration that cannot work or is unsafe.
func (c SwarmConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("swarm config: \"url\" must be an http:// or https:// URL")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) && !c.AllowPlaintext {
		return fmt.Errorf("swarm config: a SWARM-SYNC that is not on this machine needs https, or \"allowPlaintext\" set on purpose")
	}
	if c.signatureRequired() && c.SigningKeyFile == "" {
		return fmt.Errorf("swarm config: \"signingKeyFile\" is needed while \"requireSignature\" is true (it is true unless the file says false)")
	}
	if c.RefreshSeconds < 0 || (c.RefreshSeconds != 0 && (c.RefreshSeconds < 5 || c.RefreshSeconds > 3600)) {
		return fmt.Errorf("swarm config: \"refreshSeconds\" must be from 5 to 3600")
	}
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 60 {
		return fmt.Errorf("swarm config: \"timeoutSeconds\" must be from 1 to 60")
	}
	if c.MaxEntries < 0 || c.MaxEntries > 1024 {
		return fmt.Errorf("swarm config: \"maxEntries\" must be from 1 to 1024")
	}
	if c.KeyPrefix != "" && (len(c.KeyPrefix) > 64 || strings.ContainsAny(c.KeyPrefix, " \t\r\n\x00")) {
		return fmt.Errorf("swarm config: \"keyPrefix\" is too long or has white space")
	}
	return nil
}

// LoadSwarmConfig reads and validates the file; an unknown key is an error, because a misspelt "requireSignature" must not pass.
func LoadSwarmConfig(path string) (SwarmConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SwarmConfig{}, fmt.Errorf("reading swarm config %q: %w", path, err)
	}
	var c SwarmConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return SwarmConfig{}, fmt.Errorf("parsing swarm config %q: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return SwarmConfig{}, err
	}
	return c, nil
}

func readSecret(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read a secret file: %w", err)
	}
	secret := []byte(strings.TrimSpace(string(raw)))
	if len(secret) < 16 {
		return nil, fmt.Errorf("a secret file holds fewer than 16 characters")
	}
	return secret, nil
}

// announcement is the value of a key that announces a node.
type announcement struct {
	HealthURL    string `json:"healthUrl"`
	Address      string `json:"address"`
	DispatchNode string `json:"dispatchNode"`
	Watch        bool   `json:"watch"`
	Signature    string `json:"sig"`
}

// SignAnnouncement is the signature a node puts in its announcement (hex of the HMAC-SHA256 of the fields, in this order).
func SignAnnouncement(key []byte, name, healthURL, address, dispatchNode string, watch bool) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s\n%t", name, healthURL, address, dispatchNode, watch)
	return hex.EncodeToString(mac.Sum(nil))
}

// SwarmRegistry reads the announced nodes.
type SwarmRegistry struct {
	Config SwarmConfig
	// Client defaults to one bounded by the timeout of the configuration.
	Client *http.Client
	Printf func(format string, args ...any)

	key   []byte
	token string

	mu       sync.Mutex
	nodes    []watchdog.Node
	rejected int
	reads    int64
	failures int64
	lastOK   time.Time
}

// NewSwarmRegistry reads the secrets named by the configuration.
func NewSwarmRegistry(c SwarmConfig, printf func(format string, args ...any)) (*SwarmRegistry, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	r := &SwarmRegistry{Config: c, Printf: printf}
	if c.SigningKeyFile != "" {
		key, err := readSecret(c.SigningKeyFile)
		if err != nil {
			return nil, fmt.Errorf("swarm config: signing key: %w", err)
		}
		r.key = key
	}
	if c.TokenFile != "" {
		token, err := readSecret(c.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("swarm config: token: %w", err)
		}
		r.token = string(token)
	}
	return r, nil
}

func (r *SwarmRegistry) printf(format string, args ...any) {
	if r.Printf != nil {
		r.Printf(format, args...)
		return
	}
	fmt.Printf(format, args...)
}

func (r *SwarmRegistry) prefix() string {
	if r.Config.KeyPrefix != "" {
		return r.Config.KeyPrefix
	}
	return defaultKeyPrefix
}

// Parse turns the state of SWARM-SYNC into nodes. It returns the nodes that passed and how many announcements were left out.
func (r *SwarmRegistry) Parse(state map[string]json.RawMessage) ([]watchdog.Node, int) {
	prefix := r.prefix()
	limit := r.Config.MaxEntries
	if limit == 0 {
		limit = 256
	}
	keys := make([]string, 0, len(state))
	for k := range state {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var nodes []watchdog.Node
	rejected := 0
	for i, k := range keys {
		if i >= limit {
			rejected += len(keys) - i
			r.printf("[node-healing] swarm registry: %d announcement(s) past the limit of %d were left out\n", len(keys)-i, limit)
			break
		}
		name := strings.TrimPrefix(k, prefix)
		var text string
		if err := json.Unmarshal(state[k], &text); err != nil {
			rejected++
			continue
		}
		var a announcement
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&a); err != nil {
			rejected++
			r.printf("[node-healing] swarm registry: the announcement of %q is not understood and was left out\n", shorten(name))
			continue
		}
		if r.Config.signatureRequired() {
			want := SignAnnouncement(r.key, name, a.HealthURL, a.Address, a.DispatchNode, a.Watch)
			got, derr := hex.DecodeString(a.Signature)
			expected, _ := hex.DecodeString(want)
			if derr != nil || !hmac.Equal(got, expected) {
				rejected++
				r.printf("[node-healing] swarm registry: the announcement of %q has no valid signature and was left out\n", shorten(name))
				continue
			}
		}
		node, cerr := checkEntry(nodeEntry{Name: name, Address: a.Address, HealthURL: a.HealthURL, DispatchNode: a.DispatchNode, Watch: a.Watch})
		if cerr != nil {
			rejected++
			r.printf("[node-healing] swarm registry: the announcement of %q is not valid%s and was left out\n", shorten(name), strings.ReplaceAll(cerr.Error(), "\n", " "))
			continue
		}
		nodes = append(nodes, node)
	}
	return nodes, rejected
}

func shorten(text string) string {
	if len(text) > 60 {
		return text[:60] + "..."
	}
	return text
}

// Fetch reads SWARM-SYNC once. On success the announced nodes become the current ones; on failure they stay as they were.
func (r *SwarmRegistry) Fetch(ctx context.Context) error {
	client := r.Client
	if client == nil {
		timeout := time.Duration(r.Config.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(r.Config.URL, "/")+"/state", nil)
	if err != nil {
		return err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := client.Do(req)
	if err != nil {
		r.fail()
		return fmt.Errorf("cannot read the state of SWARM-SYNC: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.fail()
		return fmt.Errorf("SWARM-SYNC answered %s to GET /state", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSwarmBody+1))
	if err != nil || len(raw) > maxSwarmBody {
		r.fail()
		return fmt.Errorf("the state of SWARM-SYNC could not be read or is larger than %d bytes", maxSwarmBody)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		r.fail()
		return fmt.Errorf("the state of SWARM-SYNC is not a JSON object: %w", err)
	}
	nodes, rejected := r.Parse(state)
	r.mu.Lock()
	r.nodes, r.rejected, r.lastOK = nodes, rejected, time.Now()
	r.reads++
	r.mu.Unlock()
	return nil
}

func (r *SwarmRegistry) fail() {
	r.mu.Lock()
	r.failures++
	r.mu.Unlock()
}

// Announced returns the nodes of the last successful read.
func (r *SwarmRegistry) Announced() []watchdog.Node {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]watchdog.Node(nil), r.nodes...)
}

// Stats are the reads that worked, those that failed and the announcements left out by the last one.
func (r *SwarmRegistry) Stats() (reads, failures int64, rejected int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads, r.failures, r.rejected
}

// Merge adds the announced nodes to the static ones. A static entry wins over an announced one with the same name.
func (r *SwarmRegistry) Merge(static []watchdog.Node) []watchdog.Node {
	merged := append([]watchdog.Node(nil), static...)
	have := make(map[string]bool, len(static))
	for _, n := range static {
		have[n.Name] = true
	}
	for _, n := range r.Announced() {
		if have[n.Name] {
			continue
		}
		have[n.Name] = true
		merged = append(merged, n)
	}
	return merged
}

func sameNames(a, b []watchdog.Node) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]watchdog.Node, len(a))
	for _, n := range a {
		index[n.Name] = n
	}
	for _, n := range b {
		o, ok := index[n.Name]
		if !ok || o.Address != n.Address || o.HealthURL != n.HealthURL || o.DispatchNode != n.DispatchNode || o.Watch != n.Watch {
			return false
		}
	}
	return true
}

// Run reads SWARM-SYNC every RefreshSeconds until ctx ends and calls onChange when the announced nodes are not the ones of the
// previous read. A failed read is logged and changes nothing.
func (r *SwarmRegistry) Run(ctx context.Context, onChange func()) {
	every := time.Duration(r.Config.RefreshSeconds) * time.Second
	if every <= 0 {
		every = defaultRefreshSecs * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		before := r.Announced()
		if err := r.Fetch(ctx); err != nil {
			if ctx.Err() == nil {
				r.printf("[node-healing] swarm registry: %v; the nodes already known stay as they are\n", err)
			}
			continue
		}
		if !sameNames(before, r.Announced()) {
			r.printf("[node-healing] swarm registry: the announced nodes changed (%d now)\n", len(r.Announced()))
			onChange()
		}
	}
}
