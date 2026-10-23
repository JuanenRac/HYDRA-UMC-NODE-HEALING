// HYDRA-UMC-NODE-HEALING - node registry from the service registry: tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const samplePorts = `# Generated
services:
  - name: HYDRA-UMC-DATALAKE
    port: 8095
    health_path: /stats
    systemd_unit: hydra-umc-datalake.service
  - name: HYDRA-UMC-ORCHESTRATOR
    port: 8114
    health_path: null
    systemd_unit: hydra-umc-orchestrator.service
  - name: HYDRA-UMC-BAD
    port: 99999
    health_path: "not a path"
`

func TestParsePortsReadsPortAndHealthPathAndDropsBadValues(t *testing.T) {
	got := ParsePorts(samplePorts)
	if len(got) != 3 {
		t.Fatalf("services = %d, want 3", len(got))
	}
	if got[0] != (PortsService{Name: "HYDRA-UMC-DATALAKE", Port: 8095, HealthPath: "/stats"}) {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].HealthPath != "" || got[1].Port != 8114 {
		t.Fatalf("second = %+v", got[1])
	}
	if got[2].Port != 0 || got[2].HealthPath != "" {
		t.Fatalf("a bad port and path must be dropped: %+v", got[2])
	}
}

func TestNodesFromPortsKeepsOnlyServicesWithAHealthPathAndTheOutputLoads(t *testing.T) {
	registry, skipped, err := NodesFromPorts(ParsePorts(samplePorts))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(skipped, ",") != "HYDRA-UMC-ORCHESTRATOR,HYDRA-UMC-BAD" {
		t.Fatalf("skipped = %v", skipped)
	}
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(path, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	nodes, err := LoadNodes(path)
	if err != nil {
		t.Fatalf("the generated registry must load: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "HYDRA-UMC-DATALAKE" || nodes[0].HealthURL != "http://127.0.0.1:8095/stats" {
		t.Fatalf("nodes = %+v", nodes)
	}
}

// When the SDK is checked out next to this repository, every entry of nodes.example.json must name a
// service of the registry at the port the registry gives it.
func TestTheExampleRegistryAgreesWithTheServiceRegistry(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "HYDRA-UMC-SDK", "contracts", "ports.yaml"))
	if err != nil {
		t.Skip("the SDK checkout is not next to this repository")
	}
	known := map[string]PortsService{}
	for _, s := range ParsePorts(string(raw)) {
		known[s.Name] = s
	}
	example, err := os.ReadFile(filepath.Join("..", "..", "nodes.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Name      string `json:"name"`
		HealthURL string `json:"healthUrl"`
	}
	if err := json.Unmarshal(example, &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		s, ok := known[e.Name]
		if !ok {
			t.Errorf("%s is not a service of the registry", e.Name)
			continue
		}
		if !strings.HasPrefix(e.HealthURL, "http://127.0.0.1:") || !strings.Contains(e.HealthURL, ":"+strconv.Itoa(s.Port)+"/") {
			t.Errorf("%s: %s does not use the registry port %d", e.Name, e.HealthURL, s.Port)
		}
	}
}
