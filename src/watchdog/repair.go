// HYDRA-UMC-NODE-HEALING - reversible-only repair plans
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package watchdog

import (
	"errors"
	"fmt"
)

// RepairStep is one automatic repair action. A step that cannot be undone is
// not allowed to run automatically at all: Reversible must be true and
// Rollback must be set, otherwise RunRepair refuses the whole plan before
// touching anything.
type RepairStep struct {
	Name       string
	Reversible bool
	Apply      func() error
	Rollback   func() error
}

// RepairRecord is the decision recorded for one step, written before the step
// runs so that a crash mid-repair still leaves evidence of what would have to
// be undone.
type RepairRecord struct {
	Step     string
	Decision string // "rollback-prepared", "applied", "rolled-back", "rollback-failed"
	Detail   string
}

// ErrIrreversibleStep is returned when a plan contains a step that cannot be
// undone; nothing in the plan is executed.
var ErrIrreversibleStep = errors.New("repair plan contains a step that cannot be undone")

// RunRepair validates the plan, then applies the steps in order. Before each
// step it records that its rollback is prepared; if a step fails it rolls back
// every step already applied, newest first. record receives every decision and
// may be nil.
func RunRepair(steps []RepairStep, record func(RepairRecord)) error {
	if record == nil {
		record = func(RepairRecord) {}
	}
	for _, s := range steps {
		if s.Name == "" || s.Apply == nil {
			return fmt.Errorf("repair step %q: a name and an Apply function are required", s.Name)
		}
		if !s.Reversible || s.Rollback == nil {
			return fmt.Errorf("%w: %q", ErrIrreversibleStep, s.Name)
		}
	}

	applied := make([]RepairStep, 0, len(steps))
	for _, s := range steps {
		record(RepairRecord{Step: s.Name, Decision: "rollback-prepared"})
		if err := s.Apply(); err != nil {
			rollbackErr := rollBack(applied, record)
			if rollbackErr != nil {
				return fmt.Errorf("step %q failed: %w; rollback also failed: %v", s.Name, err, rollbackErr)
			}
			return fmt.Errorf("step %q failed and the plan was rolled back: %w", s.Name, err)
		}
		record(RepairRecord{Step: s.Name, Decision: "applied"})
		applied = append(applied, s)
	}
	return nil
}

func rollBack(applied []RepairStep, record func(RepairRecord)) error {
	var first error
	for i := len(applied) - 1; i >= 0; i-- {
		s := applied[i]
		if err := s.Rollback(); err != nil {
			record(RepairRecord{Step: s.Name, Decision: "rollback-failed", Detail: err.Error()})
			if first == nil {
				first = fmt.Errorf("%q: %w", s.Name, err)
			}
			continue
		}
		record(RepairRecord{Step: s.Name, Decision: "rolled-back"})
	}
	return first
}
