// HYDRA-UMC-NODE-HEALING - watchdog package: restore.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The cooldown and the budget of the recovery requests live in memory, so a watchdog that is restarted in a loop starts each time with a
// full budget. With a journal, every allowed request is written as a "recovery" line and can be read back at start.

package watchdog

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sort"
	"time"
)

// RecoveryEvent is the journal event of one allowed recovery request.
const RecoveryEvent = "recovery"

// PastRecovery is one recovery request read from the journal.
type PastRecovery struct {
	Node string
	At   time.Time
}

// RecentRecoveries reads the recovery requests of the journal and its rotated files that are not older than `window` before `now`,
// oldest first. A line that cannot be read is skipped (a power cut can leave half a line). A time after `now` (the clock stepped
// back) counts as `now`.
func (j *Journal) RecentRecoveries(now time.Time, window time.Duration) ([]PastRecovery, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var found []PastRecovery
	for _, name := range j.Files() {
		file, err := os.Open(name)
		if err != nil {
			return found, err
		}
		reader := bufio.NewReaderSize(file, 64*1024)
		for {
			raw, readErr := reader.ReadBytes('\n')
			var line struct {
				Event string `json:"event"`
				Node  string `json:"node"`
				Time  string `json:"time"`
			}
			if json.Unmarshal(trimNewline(raw), &line) == nil && line.Event == RecoveryEvent && line.Node != "" {
				if at, err := time.Parse(time.RFC3339, line.Time); err == nil {
					if at.After(now) {
						at = now
					}
					if now.Sub(at) < window {
						found = append(found, PastRecovery{Node: line.Node, At: at})
					}
				}
			}
			if readErr != nil {
				file.Close()
				if readErr != io.EOF {
					return found, readErr
				}
				break
			}
		}
	}
	sort.SliceStable(found, func(i, k int) bool { return found[i].At.Before(found[k].At) })
	return found, nil
}

// RestoreWindow is how far back a recovery request still matters to the cooldown or to the budget.
func (p RecoveryPolicy) RestoreWindow() time.Duration {
	if p.Cooldown > p.BudgetWindow {
		return p.Cooldown
	}
	return p.BudgetWindow
}

// Restore seeds the cooldown and the budget with requests made before this process started, oldest first.
func (r *OrchestratorReactor) Restore(past []PastRecovery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastByNode == nil {
		r.lastByNode = make(map[string]time.Time)
	}
	for _, p := range past {
		if p.At.After(r.lastByNode[p.Node]) {
			r.lastByNode[p.Node] = p.At
		}
		if r.Policy.BudgetWindow > 0 && r.now().Sub(p.At) < r.Policy.BudgetWindow {
			r.recent = append(r.recent, p.At)
		}
	}
}
