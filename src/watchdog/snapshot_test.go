// HYDRA-UMC-NODE-HEALING - watchdog package: tests of the snapshot chain, the state of the missions and the reader of retained messages
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validSnapshot(node string) Snapshot {
	return Snapshot{NodeID: node, Components: map[string]string{"node-healing": "1.0.0"}, Health: "UNHEALTHY", LastActions: []string{"a"}}
}

func TestSnapshotChain_AppendsAContinuousVerifiableChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots.jsonl")
	chain, err := NewSnapshotChain(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"a", "b", "a"} {
		if _, err := chain.Append(validSnapshot(node)); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := VerifySnapshots(path); err != nil || n != 3 {
		t.Fatalf("verify = %d, %v", n, err)
	}
	// a restart goes on from the last line: the sequence keeps growing and the chain keeps holding
	again, err := NewSnapshotChain(path)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := again.Append(validSnapshot("c"))
	if err != nil || snap.Sequence != 4 {
		t.Fatalf("after a restart: %+v, %v", snap, err)
	}
	if n, err := VerifySnapshots(path); err != nil || n != 4 {
		t.Fatalf("verify after restart = %d, %v", n, err)
	}
}

func TestSnapshotChain_AnEditedOrRemovedLineIsFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots.jsonl")
	chain, _ := NewSnapshotChain(path)
	for i := 0; i < 3; i++ {
		_, _ = chain.Append(validSnapshot("a"))
	}
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	edited := strings.Replace(lines[1], "UNHEALTHY", "HEALTHY", 1)
	_ = os.WriteFile(path, []byte(lines[0]+"\n"+edited+"\n"+lines[2]+"\n"), 0o600)
	if _, err := VerifySnapshots(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("an edited line must be found at line 2, got %v", err)
	}

	_ = os.WriteFile(path, []byte(lines[0]+"\n"+lines[2]+"\n"), 0o600)
	if _, err := VerifySnapshots(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("a removed line must be found, got %v", err)
	}
}

func TestSnapshot_ADocumentWithoutIdentityOrHealthIsRefused(t *testing.T) {
	chain, _ := NewSnapshotChain(filepath.Join(t.TempDir(), "s.jsonl"))
	for name, mutate := range map[string]func(*Snapshot){
		"no node":       func(s *Snapshot) { s.NodeID = " " },
		"no health":     func(s *Snapshot) { s.Health = "" },
		"no components": func(s *Snapshot) { s.Components = nil },
	} {
		s := validSnapshot("a")
		mutate(&s)
		if _, err := chain.Append(s); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if n, _ := VerifySnapshots(chain.Path); n != 0 {
		t.Error("a refused snapshot must not be written")
	}
}

func TestSnapshotChain_RefusesToContinueADamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	_ = os.WriteFile(path, []byte("not a snapshot\n"), 0o600)
	if _, err := NewSnapshotChain(path); err == nil {
		t.Error("a file whose last line is not a snapshot must not be continued")
	}
}

func TestSnapshotChain_KeepsOnlyTheLastActions(t *testing.T) {
	chain, _ := NewSnapshotChain(filepath.Join(t.TempDir(), "s.jsonl"))
	s := validSnapshot("a")
	s.LastActions = []string{"1", "2", "3", "4", "5", "6", "7"}
	got, err := chain.Append(s)
	if err != nil || len(got.LastActions) != maxSnapshotActions || got.LastActions[0] != "3" {
		t.Fatalf("%+v %v", got, err)
	}
}

type fakeMissions struct {
	state MissionState
	err   error
}

func (f fakeMissions) MissionState(context.Context) (MissionState, error) { return f.state, f.err }

func TestSnapshotReactor_WritesOneSnapshotPerTransitionWithTheMissionsTheNodeHolds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	chain, _ := NewSnapshotChain(path)
	r := &SnapshotReactor{
		Chain: chain, Version: "1.2.3", ConfigHash: "abc",
		Missions: fakeMissions{state: MissionState{Schema: MissionStateSchema, ByRobot: map[string][]string{"arm-3": {"j2", "j1"}}}},
		Printf:   func(string, ...any) {},
	}
	node := Node{Name: "node-a", DispatchNode: "arm-3"}
	r.OnTransition(node, StatusHealthy, StatusUnreachable, "connection refused")
	r.OnTransition(node, StatusUnreachable, StatusHealthy, "answers again")
	raw, _ := os.ReadFile(path)
	text := string(raw)
	for _, want := range []string{`"schema_version":"healing-snapshot/1"`, `"node_id":"node-a"`, `"health":"UNREACHABLE"`, `"health":"HEALTHY"`, `"missions_held":["j1","j2"]`, `"config_hash":"abc"`, `"node-healing":"1.2.3"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the snapshots lack %s:\n%s", want, text)
		}
	}
	if strings.Count(text, "\n") != 2 {
		t.Errorf("expected two snapshots, got:\n%s", text)
	}
	if n, err := VerifySnapshots(path); err != nil || n != 2 {
		t.Errorf("verify = %d, %v", n, err)
	}
}

func TestSnapshotReactor_AStateThatCannotBeReadStillWritesTheSnapshotWithoutThatField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	chain, _ := NewSnapshotChain(path)
	var said string
	r := &SnapshotReactor{Chain: chain, Version: "1", Missions: fakeMissions{err: errors.New("broker down")}, Printf: func(f string, a ...any) { said = f }}
	r.OnTransition(Node{Name: "n", DispatchNode: "arm"}, StatusHealthy, StatusUnhealthy, "x")
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "missions_held") || !strings.Contains(string(raw), `"health":"UNHEALTHY"`) {
		t.Errorf("snapshot = %s", raw)
	}
	if !strings.Contains(said, "could not be read") {
		t.Errorf("the failure was not reported: %q", said)
	}
}

func TestParseMissionState(t *testing.T) {
	good := `{"schema":"job-dispatch-state/1","counts":{"assigned":1},"by_robot":{"arm-3":["j1"]}}`
	state, err := ParseMissionState([]byte(good))
	if err != nil || len(state.Held("arm-3")) != 1 || len(state.Held("nobody")) != 0 {
		t.Fatalf("%+v %v", state, err)
	}
	for _, bad := range []string{`{}`, `{"schema":"job-dispatch-state/2"}`, `not json`, ``} {
		if _, err := ParseMissionState([]byte(bad)); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// fakeBroker answers CONNECT with CONNACK, SUBSCRIBE with SUBACK and then sends one retained PUBLISH (or none).
func fakeBroker(t *testing.T, topic string, payload []byte) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		first, _, err := mqttReadPacket(conn)
		if err != nil || first>>4 != 1 {
			return
		}
		_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00})
		first, _, err = mqttReadPacket(conn)
		if err != nil || first != 0x82 {
			return
		}
		_, _ = conn.Write([]byte{0x90, 0x03, 0x00, 0x01, 0x00})
		if payload != nil {
			packet, _ := mqttPublishPacket(topic, payload, true)
			_, _ = conn.Write(packet)
		}
		time.Sleep(200 * time.Millisecond)
	}()
	return listener.Addr().String()
}

func TestMQTTTarget_ReadRetainedTakesTheRetainedMessage(t *testing.T) {
	addr := fakeBroker(t, MissionStateTopic, []byte(`{"schema":"job-dispatch-state/1"}`))
	target := MQTTTarget{Broker: addr, ClientID: "t", Timeout: 2 * time.Second}
	raw, err := target.ReadRetained(context.Background(), MissionStateTopic)
	if err != nil || string(raw) != `{"schema":"job-dispatch-state/1"}` {
		t.Fatalf("%q %v", raw, err)
	}
	source := RetainedMissionSource{Target: MQTTTarget{Broker: fakeBroker(t, MissionStateTopic, []byte(`{"schema":"job-dispatch-state/1","by_robot":{"r":["j"]}}`)), ClientID: "t", Timeout: 2 * time.Second}}
	state, err := source.MissionState(context.Background())
	if err != nil || len(state.Held("r")) != 1 {
		t.Fatalf("%+v %v", state, err)
	}
}

func TestMQTTTarget_ReadRetainedWithNothingRetainedIsAnErrorNotAnEmptyState(t *testing.T) {
	addr := fakeBroker(t, MissionStateTopic, nil)
	target := MQTTTarget{Broker: addr, ClientID: "t", Timeout: 700 * time.Millisecond}
	if raw, err := target.ReadRetained(context.Background(), MissionStateTopic); err == nil {
		t.Fatalf("nothing retained must be an error, got %q", raw)
	}
	if _, err := (RetainedMissionSource{}).MissionState(context.Background()); err == nil {
		t.Error("a source with no broker must be an error")
	}
	if _, err := target.ReadRetained(context.Background(), "hydra/#"); err == nil {
		t.Error("a wildcard topic must be refused")
	}
}
