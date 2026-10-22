<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Integration contract
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Integration Contract

**Input:** a versioned health snapshot per node (identity, timestamp, health
state) from a real gRPC `HealthService.Check()` round-trip, or - for a registry
entry with a `healthUrl` - the answer to one HTTP `GET` (2xx healthy, a JSON
`status` of `degraded`/`unhealthy` honoured, 5xx unhealthy, no answer
unreachable, a redirect or 4xx invalid and never retried). A snapshot
missing a stable node identity, timestamp or health state is rejected;
repeated observations of the same state are idempotent (no duplicate action
on an unchanged classification - a `Reactor` fires only on a real state
*change*).

**Credentials for the probe.** An entry with a `healthUrl` may carry `"headers"` (up to eight, for example an `Authorization` header for a health route that wants a token): they are sent with every probe of that entry only, the registry file is the operator's and not part of the repository, a value is never written to a log or an error, and the headers that belong to the connection itself (`Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Upgrade`) are refused. An entry with an `address` (gRPC) cannot have them.

**Output - two real, already-implemented commands, not one decision-only
recommendation and not a future adapter:**

- **Observation command** (`watchdog.LogReactor`, default): every transition
  is logged with its `from`/`to` state and reason. No network call, no side
  effect beyond the log line.
- **Recovery command** (`watchdog.OrchestratorReactor`, active when
  `--orchestrator-url` is set): a real `POST
  {orchestrator-url}/nodes/{node.Name}/recover` to HYDRA-UMC-ORCHESTRATOR
  (`Authorization: Bearer <token>` when `-orchestrator-token` or
  `ORCHESTRATOR_TOKEN` is set),
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
not a physical action this repository performs or authorizes. The only
credential it sends is the bearer token above; deciding who may ask for a
recovery is Orchestrator's responsibility, which it enforces by refusing a
request without the token when it serves a network address.

**Real, known coupling limit:** this watchdog's own node identity (whatever a
`HealthService` reports) and Orchestrator's own mission-dispatch node naming
are the same string space only if whoever provisions `nodes.json` keeps them
consistent - this contract forwards the node's name as-is and does not
translate between the two.
