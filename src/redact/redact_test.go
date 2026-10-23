// =============================================================================
// HYDRA-UMC-NODE-HEALING - src/redact/redact_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
// =============================================================================

package redact

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type vectors struct {
	Text []struct {
		Name     string `json:"name"`
		Input    string `json:"input"`
		Expected string `json:"expected"`
	} `json:"text"`
	JSON []struct {
		Name     string `json:"name"`
		Input    any    `json:"input"`
		Expected any    `json:"expected"`
	} `json:"json"`
	Lines []struct {
		Name     string   `json:"name"`
		Input    []string `json:"input"`
		Expected []string `json:"expected"`
	} `json:"lines"`
}

func load(t *testing.T) vectors {
	t.Helper()
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSharedTextVectors(t *testing.T) {
	for _, c := range load(t).Text {
		t.Run(c.Name, func(t *testing.T) {
			if got := Secrets(c.Input); got != c.Expected {
				t.Fatalf("Secrets(%q) = %q, want %q", c.Input, got, c.Expected)
			}
		})
	}
}

func TestSharedJSONVectors(t *testing.T) {
	for _, c := range load(t).JSON {
		t.Run(c.Name, func(t *testing.T) {
			if got := JSON(c.Input); !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("JSON(%v) = %v, want %v", c.Input, got, c.Expected)
			}
		})
	}
}

func TestSharedLineVectors(t *testing.T) {
	for _, c := range load(t).Lines {
		t.Run(c.Name, func(t *testing.T) {
			if got := Lines(c.Input); !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("Lines(%q) = %q, want %q", c.Input, got, c.Expected)
			}
		})
	}
}

func TestLongRunsDoNotTakeQuadraticTime(t *testing.T) {
	long := make([]byte, 300000)
	for i := range long {
		long[i] = 'a'
	}
	_ = Secrets(string(long))
	_ = Secrets("x://" + string(long))
}
