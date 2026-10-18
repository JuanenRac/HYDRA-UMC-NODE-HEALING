// HYDRA-UMC-NODE-HEALING - node registry config tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadNodes_Valid(t *testing.T) {
	path := writeTemp(t, `[
		{"name": "HYDRA-UMC-VISION-NODE", "address": "127.0.0.1:50101"},
		{"name": "HYDRA-UMC-ORCHESTRATOR", "address": "127.0.0.1:50100"}
	]`)
	nodes, err := LoadNodes(path)
	if err != nil {
		t.Fatalf("LoadNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	if nodes[0].Name != "HYDRA-UMC-VISION-NODE" || nodes[0].Address != "127.0.0.1:50101" {
		t.Fatalf("unexpected first node: %+v", nodes[0])
	}
}

func TestLoadNodes_MissingFile(t *testing.T) {
	if _, err := LoadNodes(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestLoadNodes_EmptyArray(t *testing.T) {
	path := writeTemp(t, `[]`)
	if _, err := LoadNodes(path); err == nil {
		t.Fatal("expected an error for an empty registry, got nil")
	}
}

func TestLoadNodes_MissingAddress(t *testing.T) {
	path := writeTemp(t, `[{"name": "HYDRA-UMC-VISION-NODE"}]`)
	if _, err := LoadNodes(path); err == nil {
		t.Fatal("expected an error for an entry missing \"address\", got nil")
	}
}

func TestLoadNodes_DuplicateName(t *testing.T) {
	path := writeTemp(t, `[
		{"name": "HYDRA-UMC-VISION-NODE", "address": "127.0.0.1:50101"},
		{"name": "HYDRA-UMC-VISION-NODE", "address": "127.0.0.1:50102"}
	]`)
	_, err := LoadNodes(path)
	if err == nil {
		t.Fatal("expected an error for a duplicate \"name\", got nil")
	}
	if !strings.Contains(err.Error(), "HYDRA-UMC-VISION-NODE") {
		t.Fatalf("error %q does not name the offending duplicate", err.Error())
	}
}

func TestLoadNodes_MalformedJSON(t *testing.T) {
	path := writeTemp(t, `{not valid json`)
	if _, err := LoadNodes(path); err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}
}

func TestLoadNodes_HealthURLInsteadOfAddress(t *testing.T) {
	path := writeTemp(t, `[{"name": "HYDRA-UMC-SERVER", "healthUrl": "http://127.0.0.1:8080/api/health"}]`)
	nodes, err := LoadNodes(path)
	if err != nil {
		t.Fatalf("LoadNodes: %v", err)
	}
	if nodes[0].HealthURL != "http://127.0.0.1:8080/api/health" || nodes[0].Address != "" {
		t.Fatalf("unexpected node: %+v", nodes[0])
	}
}

func TestLoadNodes_HealthURLMustBeHTTP(t *testing.T) {
	for _, bad := range []string{"127.0.0.1:8080/health", "ftp://127.0.0.1/health", "http://", "file:///etc/passwd"} {
		path := writeTemp(t, `[{"name": "n", "healthUrl": "`+bad+`"}]`)
		if _, err := LoadNodes(path); err == nil {
			t.Errorf("healthUrl %q was accepted, want an error", bad)
		}
	}
}

func TestLoadNodes_NeitherAddressNorHealthURL(t *testing.T) {
	path := writeTemp(t, `[{"name": "n"}]`)
	_, err := LoadNodes(path)
	if err == nil || !strings.Contains(err.Error(), "healthUrl") {
		t.Fatalf("want an error naming healthUrl, got %v", err)
	}
}

func TestLoadNodes_DispatchNodeIsReadAndValidated(t *testing.T) {
	path := writeTemp(t, `[{"name":"svc","healthUrl":"http://127.0.0.1:1/health","dispatchNode":"cell-2"}]`)
	nodes, err := LoadNodes(path)
	if err != nil || nodes[0].DispatchNode != "cell-2" {
		t.Fatalf("nodes=%+v err=%v, want the dispatchNode kept", nodes, err)
	}
	for _, bad := range []string{
		`[{"name":"svc","healthUrl":"http://127.0.0.1:1/health","dispatchNode":"has space"}]`,
		`[{"name":"bad name","address":"127.0.0.1:1"}]`,
		`[{"name":"svc","address":"not-an-address"}]`,
		`[{"name":"svc","address":"127.0.0.1:1","healthUrl":"http://127.0.0.1:1/health"}]`,
	} {
		if _, err := LoadNodes(writeTemp(t, bad)); err == nil {
			t.Errorf("registry %s was accepted, want an error", bad)
		}
	}
}

func TestShippedExampleWatchesOnlyEndpointsThatExist(t *testing.T) {
	nodes, err := LoadNodes("../../nodes.example.json")
	if err != nil {
		t.Fatalf("the shipped example does not load: %v", err)
	}
	for _, node := range nodes {
		// no service of this ecosystem implements the gRPC HealthService, so the example must not use it
		if node.Address != "" || node.HealthURL == "" {
			t.Errorf("%s uses a gRPC address in the shipped example: %+v", node.Name, node)
		}
		if node.DispatchNode != "" {
			t.Errorf("%s must not carry a dispatch node in the shipped example (it names no real dispatch node)", node.Name)
		}
	}
}
