// HYDRA-UMC-NODE-HEALING - watchdog package: missionstate.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The state of the mission queue, read-only. JOB-DISPATCHER publishes it retained on hydra/jobs/state as the document
// job-dispatch-state/1 (counts by status and the ids of the jobs each robot holds). The watchdog only READS it: it learns which robots
// hold a mission before it acts, and it writes the answer in the snapshot. Pausing a robot is not done here: that goes through the
// hold of the safety zones (fencing.go) and nothing in this file moves hardware.
package watchdog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// MissionStateSchema is the version of the document this reader understands.
const MissionStateSchema = "job-dispatch-state/1"

// MissionStateTopic is where JOB-DISPATCHER publishes the state.
const MissionStateTopic = "hydra/jobs/state"

// MissionState is the part of job-dispatch-state/1 that this watchdog uses.
type MissionState struct {
	Schema  string              `json:"schema"`
	Counts  map[string]int      `json:"counts"`
	ByRobot map[string][]string `json:"by_robot"`
}

// ParseMissionState reads the document. Another version, or a document with no schema, is refused: a state that is not understood is
// not taken for "no robot holds anything".
func ParseMissionState(raw []byte) (MissionState, error) {
	var state MissionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return MissionState{}, fmt.Errorf("the state of the missions is not JSON: %w", err)
	}
	if state.Schema != MissionStateSchema {
		return MissionState{}, fmt.Errorf("the state of the missions has the schema %q, not %q", state.Schema, MissionStateSchema)
	}
	return state, nil
}

// Held returns the ids of the jobs that robot holds, sorted.
func (s MissionState) Held(robot string) []string {
	ids := append([]string(nil), s.ByRobot[robot]...)
	sort.Strings(ids)
	return ids
}

// MissionSource gives the current state of the missions.
type MissionSource interface {
	MissionState(ctx context.Context) (MissionState, error)
}

// RetainedMissionSource reads the state from the retained message on the broker, one connection per read.
type RetainedMissionSource struct {
	Target MQTTTarget
	Topic  string
}

// MissionState implements MissionSource.
func (r RetainedMissionSource) MissionState(ctx context.Context) (MissionState, error) {
	topic := r.Topic
	if topic == "" {
		topic = MissionStateTopic
	}
	if r.Target.Broker == "" {
		return MissionState{}, errors.New("no broker to read the state of the missions from")
	}
	raw, err := r.Target.ReadRetained(ctx, topic)
	if err != nil {
		return MissionState{}, err
	}
	return ParseMissionState(raw)
}
