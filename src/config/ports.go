// HYDRA-UMC-NODE-HEALING - node registry from the service registry of the ecosystem
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// contracts/ports.yaml of the SDK lists every service with its port and health path. A node registry
// does not have to be typed by hand: every service that declares a health path can be watched over
// HTTP at 127.0.0.1:<port><health path>. A service without a health path is left out and reported,
// because the watchdog has nothing honest to ask it.
package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PortsService is one entry of the service registry.
type PortsService struct {
	Name       string
	Port       int
	HealthPath string
}

var (
	portsName   = regexp.MustCompile(`^\s*-\s+name:\s*(\S+)\s*$`)
	portsField  = regexp.MustCompile(`^\s+(port|health_path):\s*(\S*)\s*$`)
	healthPathR = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
)

// ParsePorts reads the generated registry (a fixed, flat shape, not a general YAML reader).
func ParsePorts(text string) []PortsService {
	var services []PortsService
	var current *PortsService
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
			continue
		}
		if m := portsName.FindStringSubmatch(line); m != nil {
			services = append(services, PortsService{Name: m[1]})
			current = &services[len(services)-1]
			continue
		}
		if current == nil {
			continue
		}
		if m := portsField.FindStringSubmatch(line); m != nil {
			value := strings.Trim(m[2], `"'`)
			switch m[1] {
			case "port":
				if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= 65535 {
					current.Port = n
				}
			case "health_path":
				if value != "null" && healthPathR.MatchString(value) {
					current.HealthPath = value
				}
			}
		}
	}
	return services
}

// NodesFromPorts builds a node registry (JSON, the format LoadNodes reads) from the services that have a
// port and a health path, and returns the names of the services it had to leave out.
func NodesFromPorts(services []PortsService) (registry []byte, skipped []string, err error) {
	type entry struct {
		Name      string `json:"name"`
		HealthURL string `json:"healthUrl"`
	}
	var entries []entry
	for _, s := range services {
		if s.Port == 0 || s.HealthPath == "" {
			skipped = append(skipped, s.Name)
			continue
		}
		entries = append(entries, entry{Name: s.Name, HealthURL: fmt.Sprintf("http://127.0.0.1:%d%s", s.Port, s.HealthPath)})
	}
	registry, err = json.MarshalIndent(entries, "", "  ")
	return registry, skipped, err
}
