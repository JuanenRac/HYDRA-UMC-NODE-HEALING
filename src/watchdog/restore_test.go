package watchdog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestore_ThreeRecentRequestsOfTheJournalLeaveNoBudgetForAFourth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	clock := now.Add(-8 * time.Minute)
	journal, err := NewJournal(path, 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	journal.Now = func() time.Time { return clock }
	quiet := func(string, ...any) {}
	policy := DefaultRecoveryPolicy()

	first := &OrchestratorReactor{Policy: policy, Printf: quiet, Record: journal.Record, Now: func() time.Time { return clock }}
	for _, node := range []string{"a", "b", "c"} {
		if !first.allowRecovery(node) {
			t.Fatalf("the request for %s was not allowed", node)
		}
		clock = clock.Add(time.Minute)
	}
	// half a line, as a power cut leaves it
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"event":"recov`)
	_ = file.Close()

	// the watchdog starts again
	again := &OrchestratorReactor{Policy: policy, Printf: quiet, Now: func() time.Time { return now }}
	past, err := journal.RecentRecoveries(now, policy.RestoreWindow())
	if err != nil {
		t.Fatal(err)
	}
	if len(past) != 3 {
		t.Fatalf("read %v, want three requests", past)
	}
	again.Restore(past)
	if again.allowRecovery("d") {
		t.Error("a fourth request inside the window was allowed after the restart")
	}
	fresh := &OrchestratorReactor{Policy: policy, Printf: quiet, Now: func() time.Time { return now }}
	if !fresh.allowRecovery("d") {
		t.Error("without the journal the budget starts full, as before")
	}
	// the window passes: the old requests stop counting
	later, err := journal.RecentRecoveries(now.Add(time.Hour), policy.RestoreWindow())
	if err != nil || len(later) != 0 {
		t.Errorf("after an hour %v (%v), want none", later, err)
	}
}

func TestRestore_TheCooldownOfANodeSurvives(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	reactor := &OrchestratorReactor{Policy: DefaultRecoveryPolicy(), Printf: func(string, ...any) {}, Now: func() time.Time { return now }}
	reactor.Restore([]PastRecovery{{Node: "a", At: now.Add(-time.Minute)}})
	if reactor.allowRecovery("a") {
		t.Error("the node is still inside its cooldown")
	}
	if !reactor.allowRecovery("b") {
		t.Error("another node has budget left")
	}
}
