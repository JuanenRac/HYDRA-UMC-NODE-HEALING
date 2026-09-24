// HYDRA-UMC-NODE-HEALING - reversible-only repair plan tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package watchdog

import (
	"errors"
	"reflect"
	"testing"
)

func trackedStep(name string, log *[]string, failApply bool) RepairStep {
	return RepairStep{
		Name:       name,
		Reversible: true,
		Apply: func() error {
			*log = append(*log, "apply:"+name)
			if failApply {
				return errors.New("boom")
			}
			return nil
		},
		Rollback: func() error {
			*log = append(*log, "rollback:"+name)
			return nil
		},
	}
}

func TestRunRepair_AppliesEveryStepInOrderAndPreparesTheRollbackFirst(t *testing.T) {
	var log []string
	var records []RepairRecord
	err := RunRepair([]RepairStep{trackedStep("a", &log, false), trackedStep("b", &log, false)}, func(r RepairRecord) { records = append(records, r) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(log, []string{"apply:a", "apply:b"}) {
		t.Fatalf("unexpected order: %v", log)
	}
	want := []string{"rollback-prepared", "applied", "rollback-prepared", "applied"}
	for i, r := range records {
		if r.Decision != want[i] {
			t.Fatalf("record %d = %q, want %q (records: %+v)", i, r.Decision, want[i], records)
		}
	}
}

func TestRunRepair_AFailureRollsBackAppliedStepsNewestFirst(t *testing.T) {
	var log []string
	err := RunRepair([]RepairStep{trackedStep("a", &log, false), trackedStep("b", &log, false), trackedStep("c", &log, true)}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := []string{"apply:a", "apply:b", "apply:c", "rollback:b", "rollback:a"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
}

func TestRunRepair_RefusesAnIrreversibleStepBeforeTouchingAnything(t *testing.T) {
	var log []string
	irreversible := RepairStep{Name: "wipe", Reversible: false, Apply: func() error { log = append(log, "apply:wipe"); return nil }}
	err := RunRepair([]RepairStep{trackedStep("a", &log, false), irreversible}, nil)
	if !errors.Is(err, ErrIrreversibleStep) {
		t.Fatalf("expected ErrIrreversibleStep, got %v", err)
	}
	if len(log) != 0 {
		t.Fatalf("something ran before the plan was refused: %v", log)
	}
}

func TestRunRepair_AReversibleFlagWithoutARollbackIsStillRefused(t *testing.T) {
	step := RepairStep{Name: "x", Reversible: true, Apply: func() error { return nil }}
	if err := RunRepair([]RepairStep{step}, nil); !errors.Is(err, ErrIrreversibleStep) {
		t.Fatalf("expected ErrIrreversibleStep, got %v", err)
	}
}

func TestRunRepair_ARollbackFailureIsReportedAndTheRestStillRollBack(t *testing.T) {
	var log []string
	a := trackedStep("a", &log, false)
	b := trackedStep("b", &log, false)
	b.Rollback = func() error { log = append(log, "rollback:b"); return errors.New("stuck") }
	c := trackedStep("c", &log, true)
	var records []RepairRecord
	err := RunRepair([]RepairStep{a, b, c}, func(r RepairRecord) { records = append(records, r) })
	if err == nil {
		t.Fatal("expected an error")
	}
	if !reflect.DeepEqual(log[len(log)-2:], []string{"rollback:b", "rollback:a"}) {
		t.Fatalf("a failed rollback must not stop the others: %v", log)
	}
	failed := false
	for _, r := range records {
		if r.Decision == "rollback-failed" && r.Step == "b" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("the failed rollback was not recorded: %+v", records)
	}
}
