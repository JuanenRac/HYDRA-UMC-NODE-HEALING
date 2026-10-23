// HYDRA-UMC-NODE-HEALING - command line tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The binary is built once and run as an operator would: -check-config and the refusal of an open
// status endpoint are decisions of main(), not of a package.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	name := "node-healing-under-test"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

func run(t *testing.T, binary string, args ...string) (string, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Env = append(os.Environ(), "STATUS_TOKEN=", "ORCHESTRATOR_TOKEN=")
	output, err := command.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running the binary: %v", err)
		}
		code = exitErr.ExitCode()
	}
	return string(output), code
}

func TestCheckConfigValidatesTheRegistryAndExits(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`[{"name":"a","healthUrl":"http://127.0.0.1:9/health","dispatchNode":"cell-1"},{"name":"b","address":"127.0.0.1:50100"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, code := run(t, binary, "-nodes", good, "-check-config")
	if code != 0 || !strings.Contains(output, "CONFIG=PASS 2 node(s): 1 over HTTP, 1 over gRPC, 1 with a dispatchNode") {
		t.Fatalf("good registry: exit %d\n%s", code, output)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`[{"name":"a"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, code = run(t, binary, "-nodes", bad, "-check-config")
	if code == 0 || !strings.Contains(output, "CONFIG=FAIL") {
		t.Fatalf("bad registry: exit %d\n%s", code, output)
	}
	output, code = run(t, binary, "-nodes", filepath.Join(dir, "missing.json"), "-check-config")
	if code == 0 || !strings.Contains(output, "CONFIG=FAIL") {
		t.Fatalf("missing registry: exit %d\n%s", code, output)
	}
}

func TestAnOpenStatusEndpointIsRefusedWithoutAToken(t *testing.T) {
	binary := buildBinary(t)
	registry := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(registry, []byte(`[{"name":"a","healthUrl":"http://127.0.0.1:9/health"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, code := run(t, binary, "-nodes", registry, "-listen", "0.0.0.0:9190")
	if code == 0 || !strings.Contains(output, "not loopback") {
		t.Fatalf("exit %d\n%s", code, output)
	}
}
