<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Integration contract
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Integration Contract

**Input:** a versioned health snapshot per node (identity, timestamp, health
state) from a real gRPC `HealthService.Check()` round-trip. A snapshot
missing a stable node identity, timestamp or health state is rejected;
repeated observations of the same state are idempotent (no duplicate action
on an unchanged classification - a `Reactor` fires only on a real state
*change*).

**Output - two real, already-implemented commands, not one decision-only
recommendation and not a future adapter:**

- **Observation command** (`watchdog.LogReactor`, default): every transition
  is logged with its `from`/`to` state and reason. No network call, no side
  effect beyond the log line.
- **Recovery command** (`watchdog.OrchestratorReactor`, active when
  `--orchestrator-url` is set): a real, unauthenticated `POST
  {orchestrator-url}/nodes/{node.Name}/recover` to HYDRA-UMC-ORCHESTRATOR,
  sent only on a transition into `UNREACHABLE`/`INVALID`. Bounded by a 5s
  request timeout; on failure, a background retry re-attempts up to 5 times
  with 2s→30s backoff, cancelled outright by any newer transition for that
  same node (recovered on its own, or a fresh unhealthy transition restarts
  the count from attempt 1 rather than racing an older loop). See
  `src/watchdog/orchestrator_reactor_test.go` for the exact retry/
  cancellation behavior this contract commits to, verified locally.

**What this command does NOT do:** it never restarts, reflashes,
reconfigures or otherwise physically touches the node - it asks Orchestrator
to stop trusting that node with in-flight work and requeue it elsewhere,
which is a logical mission-recovery decision made entirely by Orchestrator,
not a physical action this repository performs or authorizes. It also never
authenticates the request (no credential, no token) - the receiving
endpoint's own identity/authorization boundary is Orchestrator's
responsibility, not enforced here.

**Real, known coupling limit:** this watchdog's own node identity (whatever a
`HealthService` reports) and Orchestrator's own mission-dispatch node naming
are the same string space only if whoever provisions `nodes.json` keeps them
consistent - this contract forwards the node's name as-is and does not
translate between the two.
