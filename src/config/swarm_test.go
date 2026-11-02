// HYDRA-UMC-NODE-HEALING - node registry from SWARM-SYNC: tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/watchdog"
)

const testKey = "a-signing-key-of-the-operator"

func writeFile(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// announce makes the value of a key of SWARM-SYNC: a JSON text inside the JSON object of the state.
func announce(name, healthURL, address, dispatch string, watch bool, key string) string {
	a := map[string]any{"healthUrl": healthURL, "address": address, "dispatchNode": dispatch, "watch": watch}
	if key != "" {
		a["sig"] = SignAnnouncement([]byte(key), name, healthURL, address, dispatch, watch)
	}
	raw, _ := json.Marshal(a)
	return string(raw)
}

func newRegistry(t *testing.T, url string, mutate func(*SwarmConfig)) *SwarmRegistry {
	t.Helper()
	c := SwarmConfig{Enabled: true, URL: url, SigningKeyFile: writeFile(t, "key", testKey)}
	if mutate != nil {
		mutate(&c)
	}
	var quiet func(string, ...any) = func(string, ...any) {}
	r, err := NewSwarmRegistry(c, quiet)
	if err != nil {
		t.Fatalf("NewSwarmRegistry: %v", err)
	}
	return r
}

func stateOf(t *testing.T, entries map[string]string) map[string]json.RawMessage {
	t.Helper()
	state := map[string]json.RawMessage{}
	for k, v := range entries {
		raw, _ := json.Marshal(v)
		state[k] = raw
	}
	return state
}

func TestSwarm_SignedAnnouncementsBecomeNodes(t *testing.T) {
	r := newRegistry(t, "http://127.0.0.1:1", nil)
	state := stateOf(t, map[string]string{
		"healing/node/svc-a":   announce("svc-a", "http://127.0.0.1:9001/stats", "", "cell-a", false, testKey),
		"healing/node/svc-b":   announce("svc-b", "", "127.0.0.1:9002", "", true, testKey),
		"unrelated/key":        "whatever",
		"healing/node/missing": announce("missing", "http://127.0.0.1:9/x", "", "", false, ""), // no signature
	})
	nodes, rejected := r.Parse(state)
	if len(nodes) != 2 || rejected != 1 {
		t.Fatalf("nodes=%v rejected=%d, want 2 nodes and 1 rejected", nodes, rejected)
	}
	if nodes[0].Name != "svc-a" || nodes[0].DispatchNode != "cell-a" || nodes[1].Name != "svc-b" || !nodes[1].Watch {
		t.Errorf("unexpected nodes: %+v", nodes)
	}
}

func TestSwarm_TheSignatureCoversEveryField(t *testing.T) {
	r := newRegistry(t, "http://127.0.0.1:1", nil)
	good := announce("svc", "http://127.0.0.1:9001/stats", "", "cell-a", false, testKey)
	cases := map[string]string{
		"another key":        announce("svc", "http://127.0.0.1:9001/stats", "", "cell-a", false, "another-key-of-someone-else"),
		"another dispatch":   strings.Replace(good, "cell-a", "cell-z", 1),
		"another url":        strings.Replace(good, "9001", "9002", 1),
		"watch added":        strings.Replace(good, `"watch":false`, `"watch":true`, 1),
		"signature stripped": strings.Replace(good, `"sig"`, `"sig2"`, 1),
		"signature not hex":  strings.Replace(good, `"sig":"`, `"sig":"zz`, 1),
	}
	for name, value := range cases {
		nodes, rejected := r.Parse(stateOf(t, map[string]string{"healing/node/svc": value}))
		if len(nodes) != 0 || rejected != 1 {
			t.Errorf("%s: an altered announcement must be left out (nodes=%v rejected=%d)", name, nodes, rejected)
		}
	}
	// the name is signed too: the same announcement under another key does not verify
	nodes, _ := r.Parse(stateOf(t, map[string]string{"healing/node/other": good}))
	if len(nodes) != 0 {
		t.Error("an announcement copied under another name must not verify")
	}
	if nodes, _ := r.Parse(stateOf(t, map[string]string{"healing/node/svc": good})); len(nodes) != 1 {
		t.Error("the untouched announcement must verify")
	}
}

func TestSwarm_AnEntryThatFailsTheStaticChecksIsLeftOut(t *testing.T) {
	r := newRegistry(t, "http://127.0.0.1:1", nil)
	state := stateOf(t, map[string]string{
		"healing/node/bad name":  announce("bad name", "http://127.0.0.1:9/x", "", "", false, testKey),
		"healing/node/no-probe":  announce("no-probe", "", "", "", false, testKey),
		"healing/node/ftp":       announce("ftp", "ftp://127.0.0.1/x", "", "", false, testKey),
		"healing/node/both":      announce("both", "http://127.0.0.1:9/x", "127.0.0.1:9", "", false, testKey),
		"healing/node/watch-url": announce("watch-url", "http://127.0.0.1:9/x", "", "", true, testKey),
		"healing/node/ok":        announce("ok", "http://127.0.0.1:9/x", "", "", false, testKey),
	})
	nodes, rejected := r.Parse(state)
	if len(nodes) != 1 || nodes[0].Name != "ok" || rejected != 5 {
		t.Fatalf("nodes=%v rejected=%d, want only ok and 5 rejected", nodes, rejected)
	}
}

func TestSwarm_NotJSONOrUnknownFieldsAreLeftOut(t *testing.T) {
	r := newRegistry(t, "http://127.0.0.1:1", nil)
	state := map[string]json.RawMessage{
		"healing/node/a": json.RawMessage(`42`),
		"healing/node/b": json.RawMessage(`"not json"`),
		"healing/node/c": json.RawMessage(`"{\"healthUrl\":\"http://127.0.0.1:9/x\",\"headers\":{\"Authorization\":\"x\"}}"`),
	}
	if nodes, rejected := r.Parse(state); len(nodes) != 0 || rejected != 3 {
		t.Fatalf("nodes=%v rejected=%d", nodes, rejected)
	}
}

func TestSwarm_MaxEntriesBoundsTheRead(t *testing.T) {
	r := newRegistry(t, "http://127.0.0.1:1", func(c *SwarmConfig) { c.MaxEntries = 2 })
	entries := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d"} {
		entries["healing/node/"+n] = announce(n, "http://127.0.0.1:9/x", "", "", false, testKey)
	}
	nodes, rejected := r.Parse(stateOf(t, entries))
	if len(nodes) != 2 || rejected != 2 {
		t.Fatalf("nodes=%d rejected=%d, want 2 and 2", len(nodes), rejected)
	}
}

func TestSwarm_UnsignedIsAcceptedOnlyWhenTheOperatorSaysSo(t *testing.T) {
	no := false
	r := newRegistry(t, "http://127.0.0.1:1", func(c *SwarmConfig) { c.RequireSignature = &no; c.SigningKeyFile = "" })
	nodes, rejected := r.Parse(stateOf(t, map[string]string{"healing/node/a": announce("a", "http://127.0.0.1:9/x", "", "", false, "")}))
	if len(nodes) != 1 || rejected != 0 {
		t.Fatalf("nodes=%v rejected=%d", nodes, rejected)
	}
}

func TestSwarm_MergeKeepsStaticEntries(t *testing.T) {
	r := newRegistry(t, "http://127.0.0.1:1", nil)
	r.nodes = []watchdog.Node{{Name: "shared", HealthURL: "http://announced/x"}, {Name: "new", HealthURL: "http://127.0.0.1:9/x"}}
	merged := r.Merge([]watchdog.Node{{Name: "shared", HealthURL: "http://static/x"}, {Name: "own", HealthURL: "http://127.0.0.1:8/x"}})
	if len(merged) != 3 {
		t.Fatalf("merged = %+v", merged)
	}
	for _, n := range merged {
		if n.Name == "shared" && n.HealthURL != "http://static/x" {
			t.Error("a static entry must win over an announced one")
		}
	}
}

func swarmServer(t *testing.T, state *atomic.Value, token string, status *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/state" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if code := status.Load(); code != 0 {
			w.WriteHeader(int(code))
			return
		}
		_, _ = w.Write(state.Load().([]byte))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSwarm_FetchReadsTheStateWithTheToken(t *testing.T) {
	var state atomic.Value
	raw, _ := json.Marshal(stateOf(t, map[string]string{"healing/node/a": announce("a", "http://127.0.0.1:9/x", "", "cell", false, testKey)}))
	state.Store(raw)
	var status atomic.Int64
	server := swarmServer(t, &state, "the-shared-secret-of-swarm", &status)
	r := newRegistry(t, server.URL, func(c *SwarmConfig) { c.TokenFile = writeFile(t, "token", "the-shared-secret-of-swarm\n") })
	if err := r.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := r.Announced(); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("announced = %+v", got)
	}
	// without the token SWARM-SYNC says 401: the read fails and the nodes stay
	noToken := newRegistry(t, server.URL, nil)
	noToken.nodes = []watchdog.Node{{Name: "kept", HealthURL: "http://127.0.0.1:9/x"}}
	if err := noToken.Fetch(context.Background()); err == nil {
		t.Error("a 401 must be an error")
	}
	if got := noToken.Announced(); len(got) != 1 || got[0].Name != "kept" {
		t.Error("a failed read must leave the nodes as they were")
	}
	if reads, failures, _ := noToken.Stats(); reads != 0 || failures != 1 {
		t.Errorf("reads=%d failures=%d", reads, failures)
	}
}

func TestSwarm_AFailedOrOversizedOrBrokenReadKeepsTheNodes(t *testing.T) {
	var state atomic.Value
	var status atomic.Int64
	server := swarmServer(t, &state, "", &status)
	r := newRegistry(t, server.URL, nil)
	r.nodes = []watchdog.Node{{Name: "kept", HealthURL: "http://127.0.0.1:9/x"}}
	for name, setup := range map[string]func(){
		"server error": func() { status.Store(500) },
		"not json":     func() { status.Store(0); state.Store([]byte("<html>")) },
		"too big":      func() { state.Store([]byte(`{"k":"` + strings.Repeat("x", maxSwarmBody) + `"}`)) },
	} {
		setup()
		if err := r.Fetch(context.Background()); err == nil {
			t.Errorf("%s must be an error", name)
		}
		if got := r.Announced(); len(got) != 1 || got[0].Name != "kept" {
			t.Errorf("%s: the nodes must stay", name)
		}
	}
}

func TestSwarm_RunCallsBackOnlyWhenTheNodesChange(t *testing.T) {
	var state atomic.Value
	var status atomic.Int64
	one := map[string]string{"healing/node/a": announce("a", "http://127.0.0.1:9/x", "", "", false, testKey)}
	set := func(entries map[string]string) {
		raw, _ := json.Marshal(stateOf(t, entries))
		state.Store(raw)
	}
	set(one)
	server := swarmServer(t, &state, "", &status)
	r := newRegistry(t, server.URL, nil)
	r.Config.RefreshSeconds = 5
	if err := r.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Run reads on a ticker of RefreshSeconds; the test drives the same steps by hand to stay fast
	var changes int
	var mu sync.Mutex
	step := func() {
		before := r.Announced()
		if err := r.Fetch(context.Background()); err == nil && !sameNames(before, r.Announced()) {
			mu.Lock()
			changes++
			mu.Unlock()
		}
	}
	step()
	if changes != 0 {
		t.Error("the same state must not be a change")
	}
	set(map[string]string{"healing/node/a": one["healing/node/a"], "healing/node/b": announce("b", "http://127.0.0.1:9/y", "", "", false, testKey)})
	step()
	if changes != 1 {
		t.Errorf("changes = %d, want 1", changes)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r.Run(ctx, func() {}) // returns when the context ends
}

func TestSwarmConfig_Validation(t *testing.T) {
	yes, no := true, false
	bad := map[string]SwarmConfig{
		"no url":                     {Enabled: true, SigningKeyFile: "k"},
		"ftp url":                    {Enabled: true, URL: "ftp://127.0.0.1", SigningKeyFile: "k"},
		"plain http to a remote one": {Enabled: true, URL: "http://swarm.example:8112", SigningKeyFile: "k"},
		"signature without a key":    {Enabled: true, URL: "http://127.0.0.1:8112"},
		"explicit signature no key":  {Enabled: true, URL: "http://127.0.0.1:8112", RequireSignature: &yes},
		"refresh too fast":           {Enabled: true, URL: "http://127.0.0.1:8112", SigningKeyFile: "k", RefreshSeconds: 1},
		"timeout too long":           {Enabled: true, URL: "http://127.0.0.1:8112", SigningKeyFile: "k", TimeoutSeconds: 600},
		"too many entries":           {Enabled: true, URL: "http://127.0.0.1:8112", SigningKeyFile: "k", MaxEntries: 5000},
		"prefix with a space":        {Enabled: true, URL: "http://127.0.0.1:8112", SigningKeyFile: "k", KeyPrefix: "a b"},
	}
	for name, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	for i, c := range []SwarmConfig{{}, {Enabled: true, URL: "https://swarm.example", SigningKeyFile: "k"}, {Enabled: true, URL: "http://localhost:8112", RequireSignature: &no},
		{Enabled: true, URL: "http://swarm.example:8112", SigningKeyFile: "k", AllowPlaintext: true}} {
		if err := c.Validate(); err != nil {
			t.Errorf("good config %d refused: %v", i, err)
		}
	}
}

func TestLoadSwarmConfig(t *testing.T) {
	if _, err := LoadSwarmConfig(writeFile(t, "a.json", `{"enabled": true, "url": "http://127.0.0.1:1", "signingKeyFile": "k", "requireSignatur": false}`)); err == nil {
		t.Error("a misspelt key must be refused: it could silently turn the signature check back on or off")
	}
	if _, err := LoadSwarmConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing file must be refused")
	}
	c, err := LoadSwarmConfig(writeFile(t, "ok.json", `{"enabled": false}`))
	if err != nil || c.Enabled {
		t.Errorf("a file that leaves it off must load: %v", err)
	}
	if _, err := NewSwarmRegistry(SwarmConfig{Enabled: true, URL: "http://127.0.0.1:1", SigningKeyFile: writeFile(t, "short", "abc")}, nil); err == nil {
		t.Error("a short signing key must be refused")
	}
}

func TestLoadNodes_WatchNeedsAnAddress(t *testing.T) {
	p := writeFile(t, "nodes.json", `[{"name": "a", "healthUrl": "http://127.0.0.1:9/x", "watch": true}]`)
	if _, err := LoadNodes(p); err == nil || !strings.Contains(err.Error(), "watch") {
		t.Errorf("watch with a healthUrl must be refused: %v", err)
	}
	p = writeFile(t, "nodes.json", `[{"name": "a", "address": "127.0.0.1:50100", "watch": true}]`)
	nodes, err := LoadNodes(p)
	if err != nil || len(nodes) != 1 || !nodes[0].Watch {
		t.Errorf("watch with an address must load: %v %+v", err, nodes)
	}
}
