// HYDRA-UMC-NODE-HEALING - watchdog package: the recovery request against the real HYDRA-UMC-ORCHESTRATOR binary
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// orchestratorBinary finds the real binary: HYDRA_UMC_ORCHESTRATOR_BIN, or the build of the repository checked out next to this one.
// The test is skipped when there is none: building a Rust project is not this repository's CI, but wherever the binary exists the
// request this reactor sends is held against what the real server answers, not against a fake written from the same understanding.
func orchestratorBinary() string {
	if path := os.Getenv("HYDRA_UMC_ORCHESTRATOR_BIN"); path != "" {
		return path
	}
	name := "hydra-umc-orchestrator"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, profile := range []string{"release", "debug"} {
		candidate := filepath.Join("..", "..", "..", "HYDRA-UMC-ORCHESTRATOR", "target", profile, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("no free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func call(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	text, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(text)
}

func TestOrchestratorReactor_AgainstTheRealOrchestratorBinary(t *testing.T) {
	binary := orchestratorBinary()
	if binary == "" {
		t.Skip("no HYDRA-UMC-ORCHESTRATOR binary: set HYDRA_UMC_ORCHESTRATOR_BIN or build the repository next to this one")
	}
	port := freeLoopbackPort(t)
	token := "contract-test-token-of-enough-length"
	command := exec.Command(binary, "serve", "--addr", "127.0.0.1", "--port", fmt.Sprint(port), "--token", token, "--data-dir", t.TempDir())
	if err := command.Start(); err != nil {
		t.Fatalf("cannot start the orchestrator: %v", err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if response, err := (&http.Client{Timeout: time.Second}).Get(base + "/health"); err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the orchestrator did not answer /health")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// two missions on two dispatch nodes: only the one on the node that fell must go back to Pending
	for _, mission := range []string{"contract-1", "contract-2"} {
		if status, body := call(t, "POST", base+"/missions", token, `{"id":"`+mission+`"}`); status != 200 {
			t.Fatalf("create %s: %d %s", mission, status, body)
		}
	}
	call(t, "POST", base+"/missions/contract-1/dispatch", token, `{"node":"cell-a"}`)
	call(t, "POST", base+"/missions/contract-2/dispatch", token, `{"node":"cell-b"}`)

	var lines []string
	reactor := &OrchestratorReactor{
		BaseURL: base,
		Token:   token,
		Printf:  func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
		Policy:  RecoveryPolicy{RequireDispatchNode: true},
		Context: context.Background(),
	}
	watched := Node{Name: "HYDRA-UMC-VOICE-UI", DispatchNode: "cell-a"}
	reactor.OnTransition(watched, StatusHealthy, StatusUnreachable, "connection refused")

	deadline = time.Now().Add(10 * time.Second)
	for reactor.RecoveryRequests() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if reactor.RecoveryRequests() != 1 {
		t.Fatalf("the reactor sent %d recovery requests, want 1; it said: %v", reactor.RecoveryRequests(), lines)
	}
	if _, body := call(t, "GET", base+"/missions/contract-1", token, ""); !strings.Contains(body, "Pending") {
		t.Errorf("the mission on the fallen node did not go back to Pending: %s", body)
	}
	if _, body := call(t, "GET", base+"/missions/contract-2", token, ""); strings.Contains(body, "Pending") {
		t.Errorf("the mission on the other node was requeued too: %s", body)
	}

	// a reactor with the wrong token is refused for good, and says so, instead of retrying forever
	wrong := &OrchestratorReactor{BaseURL: base, Token: "not-the-token", Printf: func(string, ...any) {}, Policy: RecoveryPolicy{RequireDispatchNode: true}}
	wrong.OnTransition(watched, StatusHealthy, StatusUnreachable, "connection refused")
	deadline = time.Now().Add(10 * time.Second)
	for wrong.RecoveryRequests() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if wrong.RecoveryRequests() != 1 {
		t.Errorf("the reactor with the wrong token sent %d requests, want exactly 1", wrong.RecoveryRequests())
	}
}
