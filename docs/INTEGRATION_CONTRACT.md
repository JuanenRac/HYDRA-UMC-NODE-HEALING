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

**What the watchdog exposes.** The snapshot is held in memory; there is no versioned snapshot document on disk or on the wire. What a consumer can read is the status endpoint (`-listen`): `GET /health` for liveness and `GET /metrics` in Prometheus text (status, last check, duration, checks, transitions per node, and the recovery requests sent). The journal (`-journal`) keeps the transitions.

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

**Events over MQTT (optional, off by default: `--events-config FILE`, see `events.example.json`).** With `mqtt.enabled` in the file, a third reactor runs beside the two above and never delays the polling (a bounded queue, one worker, a connection per message; a full queue drops and counts):

- `<topicPrefix>/<node>/state` (default prefix `hydra/health`, retained, QoS 0): every transition as an event envelope of the SDK (`event-envelope.schema.json`): `schema_version` `"1.0"`, `event_id`, `type` `node.health.transition`, `source` `hydra-umc-node-healing`, `timestamp_utc`, `sequence` (from 0 in each run), plus `node`, `from`, `to` and `detail` (redacted, at most 500 characters). The node name is made safe for a topic level.
- `<topicPrefix>/<node>/incident-proposal` (not retained), only with `incidentProposals.enabled`: when a node stays in `UNREACHABLE`, `INVALID` or `UNHEALTHY` (and `DEGRADED` with `includeDegraded`) for `sustainedSeconds` without any other transition, ONE message in the form of the SDK's `maintenance-incident.schema.json` (`redactionLevel` `sanitized`, `requestedBy` `null`, `severity` `critical` for UNREACHABLE and INVALID and `warning` otherwise, the state topic as its evidence). A node that comes back in time proposes nothing, and a new episode proposes again.

The proposal is only a proposal: this watchdog does not open, store or act on an incident, and it still never stops or restarts anything. The consumer side (a maintenance agent that subscribes to the topic and turns a proposal into an incident for a person to review) is not built yet; until it is, the topic is read by whoever subscribes to it. A broker that is not on this machine is refused unless `mqtt.tls` is on or `mqtt.allowPlaintext` says on purpose that plain text is fine. These messages have been checked against a stand-in broker only, never against a real broker or a real consumer. The gRPC `HealthService` is still not implemented by the services of the ecosystem, which answer over HTTP.
