// HYDRA-UMC-NODE-HEALING - watchdog package: journal_test.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
package watchdog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		out = append(out, record)
	}
	return out
}

func newTestJournal(t *testing.T, maxBytes int64, backups int) *Journal {
	t.Helper()
	journal, err := NewJournal(filepath.Join(t.TempDir(), "state", "journal.jsonl"), maxBytes, backups)
	if err != nil {
		t.Fatal(err)
	}
	journal.Now = func() time.Time { return time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC) }
	return journal
}

func TestJournalWritesChainedLinesWithTimeAndEvent(t *testing.T) {
	journal := newTestJournal(t, DefaultJournalMaxBytes, 2)
	if err := journal.Record("start", map[string]any{"nodes": 2}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Record("log", map[string]any{"message": "hello"}); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, journal.Path)
	if len(lines) != 2 || lines[0]["event"] != "start" || lines[0]["time"] != "2000-01-02T03:04:05Z" {
		t.Fatalf("unexpected lines: %v", lines)
	}
	if lines[0]["prev"] != journalGenesis || lines[1]["prev"] == journalGenesis {
		t.Fatalf("the chain does not start at the genesis value: %v", lines)
	}
	if count, err := journal.VerifyJournal(); err != nil || count != 2 {
		t.Fatalf("verify: %d %v", count, err)
	}
}

func TestJournalCallerCannotOverwriteTheChainFields(t *testing.T) {
	journal := newTestJournal(t, DefaultJournalMaxBytes, 1)
	_ = journal.Record("log", map[string]any{"prev": "forged", "time": "forged", "event": "forged"})
	line := readLines(t, journal.Path)[0]
	if line["prev"] == "forged" || line["time"] == "forged" || line["event"] != "log" {
		t.Fatalf("fields were taken from the caller: %v", line)
	}
}

func TestJournalDetectsARemovedAndAnEditedLine(t *testing.T) {
	journal := newTestJournal(t, DefaultJournalMaxBytes, 1)
	for i := 0; i < 4; i++ {
		_ = journal.Record("log", map[string]any{"i": i})
	}
	data, _ := os.ReadFile(journal.Path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	removed := strings.Join(append(append([]string{}, lines[:1]...), lines[2:]...), "\n") + "\n"
	_ = os.WriteFile(journal.Path, []byte(removed), 0o640)
	if _, err := journal.VerifyJournal(); err == nil || !strings.Contains(err.Error(), "journal.jsonl:2") {
		t.Fatalf("a removed line was not found: %v", err)
	}
	edited := strings.Replace(string(data), `"i":1`, `"i":9`, 1)
	_ = os.WriteFile(journal.Path, []byte(edited), 0o640)
	if _, err := journal.VerifyJournal(); err == nil {
		t.Fatal("an edited line was not found")
	}
}

func TestJournalRotatesKeepsTheChainAndKeepsOnlyTheBackups(t *testing.T) {
	journal := newTestJournal(t, 1024, 2)
	for i := 0; i < 80; i++ {
		if err := journal.Record("log", map[string]any{"message": strings.Repeat("x", 100), "i": i}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(journal.Path + ".2"); err != nil {
		t.Fatalf("no second rotated file: %v", err)
	}
	if _, err := os.Stat(journal.Path + ".3"); err == nil {
		t.Fatal("more rotated files than asked for")
	}
	if info, _ := os.Stat(journal.Path); info.Size() > 1024 {
		t.Fatalf("the current file is over the limit: %d", info.Size())
	}
	if _, err := journal.VerifyJournal(); err != nil {
		t.Fatalf("the chain broke at a rotation: %v", err)
	}
}

func TestJournalContinuesTheChainAfterARestart(t *testing.T) {
	first := newTestJournal(t, 4096, 2)
	_ = first.Record("start", nil)
	_ = first.Record("log", map[string]any{"message": "before"})
	second := &Journal{Path: first.Path, MaxBytes: 4096, Backups: 2}
	if err := second.Record("start", nil); err != nil {
		t.Fatal(err)
	}
	if count, err := second.VerifyJournal(); err != nil || count != 3 {
		t.Fatalf("verify after restart: %d %v", count, err)
	}
}

func TestJournalPrintfForwardsAndRecordsWithoutTheNewline(t *testing.T) {
	journal := newTestJournal(t, DefaultJournalMaxBytes, 1)
	var shown string
	printf := journal.Printf(func(format string, args ...any) { shown = strings.TrimSpace(format) })
	printf("[node-healing] %s: %s -> %s\n", "robot-1", "OK", "UNREACHABLE")
	line := readLines(t, journal.Path)[0]
	if line["event"] != "log" || line["message"] != "[node-healing] robot-1: OK -> UNREACHABLE" {
		t.Fatalf("unexpected line: %v", line)
	}
	if shown == "" {
		t.Fatal("the text was not forwarded")
	}
}

func TestJournalFailureIsReportedAndNeverPanics(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	_ = os.WriteFile(blocker, []byte("file"), 0o640)
	journal, _ := NewJournal(filepath.Join(blocker, "journal.jsonl"), DefaultJournalMaxBytes, 1)
	if err := journal.Record("log", nil); err == nil {
		t.Fatal("writing under a file should fail")
	}
	journal.Printf(func(string, ...any) {})("still fine\n")
}

func TestNewJournalRefusesBadOptions(t *testing.T) {
	for _, c := range []struct {
		path    string
		max     int64
		backups int
	}{{"", 2048, 1}, {"a", 10, 1}, {"a", 2048, -1}, {"a", 2048, 101}} {
		if _, err := NewJournal(c.path, c.max, c.backups); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
}

func TestJournalNeverKeepsASecret(t *testing.T) {
	journal := newTestJournal(t, DefaultJournalMaxBytes, 2)
	if err := journal.Record("transition", map[string]any{
		"detail": "probe said password=hunter2",
		"log":    []string{"Authorization: Bearer abc.def.ghi"},
		"nested": map[string]any{"token": "s3cr3t", "ok": "fine"},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"hunter2", "abc.def.ghi", "s3cr3t"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("the journal kept %q: %s", leaked, data)
		}
	}
	if !strings.Contains(string(data), "fine") {
		t.Fatalf("an innocent value was lost: %s", data)
	}
	if _, err := journal.VerifyJournal(); err != nil {
		t.Fatalf("the chain is broken: %v", err)
	}
}
