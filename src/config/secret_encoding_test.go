// HYDRA-UMC-NODE-HEALING - config/secret_encoding_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestASecretFileMayCarryAByteOrderMarkButNoOtherEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("\ufeffa-shared-secret-of-the-swarm\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readSecret(path); err != nil || string(got) != "a-shared-secret-of-the-swarm" {
		t.Fatalf("with a mark: %q, %v", got, err)
	}
	if err := os.WriteFile(path, append([]byte{0xFF, 0xFE}, []byte("a\x00b\x00c\x00d\x00e\x00f\x00g\x00h\x00i\x00")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(path); err == nil {
		t.Fatal("a UTF-16 secret file was accepted")
	}
}
