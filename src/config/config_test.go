// HYDRA-UMC-NODE-HEALING - node registry config tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
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
