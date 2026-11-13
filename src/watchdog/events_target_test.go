// HYDRA-UMC-NODE-HEALING - watchdog package: events_target_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventsConfig_TargetReadsAndTrimsTheCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user")
	pass := filepath.Join(dir, "pass")
	if err := os.WriteFile(user, []byte("  healer\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pass, []byte("s3cret-value\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultEventsConfig()
	cfg.MQTT.Broker = "broker.example:8883"
	cfg.MQTT.TLS = true
	cfg.MQTT.CAFile = "/etc/ca.pem"
	cfg.MQTT.ClientID = "node-healing-test"
	cfg.MQTT.UsernameFile, cfg.MQTT.PasswordFile = user, pass
	cfg.MQTT.TimeoutSeconds = 7
	target, err := cfg.Target()
	if err != nil {
		t.Fatal(err)
	}
	want := MQTTTarget{Broker: "broker.example:8883", TLS: true, CAFile: "/etc/ca.pem", ClientID: "node-healing-test", Username: "healer", Password: "s3cret-value", Timeout: 7 * time.Second}
	if target != want {
		t.Errorf("got %+v, want %+v", target, want)
	}
}

func TestEventsConfig_TargetWithoutCredentialFilesHasNoCredentials(t *testing.T) {
	cfg := DefaultEventsConfig()
	cfg.MQTT.Broker = "127.0.0.1:1883"
	target, err := cfg.Target()
	if err != nil {
		t.Fatal(err)
	}
	if target.Username != "" || target.Password != "" {
		t.Errorf("credentials from nowhere: %+v", target)
	}
}

func TestEventsConfig_ATargetWhoseCredentialFileIsMissingIsRefusedAndSaysWhich(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	cfg := DefaultEventsConfig()
	cfg.MQTT.UsernameFile = missing
	if _, err := cfg.Target(); err == nil || !strings.Contains(err.Error(), "usernameFile") {
		t.Errorf("the user name file: %v", err)
	}
	cfg = DefaultEventsConfig()
	cfg.MQTT.PasswordFile = missing
	if _, err := cfg.Target(); err == nil || !strings.Contains(err.Error(), "passwordFile") {
		t.Errorf("the password file: %v", err)
	}
	if text, err := readSecretFile(""); text != "" || err != nil {
		t.Errorf("no file name means no secret and no error: %q %v", text, err)
	}
}
