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

**The versioned snapshot (optional, off by default: `--snapshot-file PATH`).** Every change of state of a node is written as one `healing-snapshot/1` document, a JSON object per line, to an append-only file: `schema_version`, `node_id`, `sequence` (grows by one with every snapshot of the file and carries on after a restart), `components` (the version of the watchdog), `config_hash` (SHA-256 of the registry file), `health` (the state the watchdog holds), `last_actions` (the last five things that happened to the node, oldest first), `missions_held` (the jobs the node holds, only when the state of the missions is read, see below), `prev_hash` and `hash`. Each snapshot carries the hash of the one before it, so a line removed or edited afterwards breaks the chain; `--snapshot-verify PATH` checks a file and prints `SNAPSHOT_CHAIN=PASS` or `SNAPSHOT_CHAIN=FAIL` with the first line that does not fit. The document has no date or clock reading. One without a node identity, a sequence, component versions or a health state is refused. A snapshot that cannot be written is reported and never stops the watchdog.

**The state of the missions (optional, read only: `missionState` in the events file).** With `mqtt.enabled` and `missionState.enabled`, the watchdog reads the document `job-dispatch-state/1` that JOB-DISPATCHER keeps retained on `hydra/jobs/state` (counts by status and the jobs each robot holds) on the same broker, and names in the snapshot the jobs a node holds (the registry entry needs a `dispatchNode`). It only reads: a state it cannot read or does not understand is left out and reported, and is never taken for "no robot holds anything". The pause of a robot does not go through this reading: the fencing asks the cell for a hold (`POST holdUrl`, the body carries `node`, `robot` and `reason`, which is what `POST /hold` of HYDRA-UMC-SAFETY-ZONES takes with the identity of the server), and nothing moves hardware unless the operator started the safety zones with their own switch for it.

**What the watchdog exposes.** Besides the snapshot file above, what a consumer can read is the status endpoint (`-listen`): `GET /health` for liveness and `GET /metrics` in Prometheus text (status, last check, duration, checks, transitions per node, and the recovery requests sent). The journal (`-journal`) keeps the transitions.

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

The proposal is only a proposal: this watchdog does not open, store or act on an incident, and it still never stops or restarts anything. The consumer side (a maintenance agent that subscribes to the topic and turns a proposal into an incident for a person to review) is not built yet; until it is, the topic is read by whoever subscribes to it. A broker that is not on this machine is refused unless `mqtt.tls` is on or `mqtt.allowPlaintext` says on purpose that plain text is fine. These messages have been checked against a stand-in broker only, never against a real broker or a real consumer. The gRPC `HealthService` is served by HYDRA-UMC-ORCHESTRATOR and HYDRA-UMC-JOB-DISPATCHER (loopback); the other services answer over HTTP.

**Registry announced in the state of SWARM-SYNC (optional, off by default: `-swarm-config FILE`, see `swarm.example.json`).** The watchdog reads `GET /state` of SWARM-SYNC (the bearer token of `tokenFile` when SWARM-SYNC has a shared secret) every `refreshSeconds` and adds the nodes announced under the keys `healing/node/<name>` (`keyPrefix`). The value of a key is a text that holds a JSON object `{"healthUrl": "...", "address": "...", "dispatchNode": "...", "watch": false, "sig": "<hex>"}`; `sig` is the HMAC-SHA256, with the key of `signingKeyFile`, of `name \n healthUrl \n address \n dispatchNode \n watch` (the Go function `config.SignAnnouncement`). An announcement is left out, and counted, when its signature does not verify, when it has a key this contract does not know (an announcement cannot carry `headers`: they may hold a secret and stay in the registry file) or when it fails the checks of a line of the file; `requireSignature: false` is possible and says so in the file. At most `maxEntries` announcements are taken per read and the answer is read up to 1 MiB. A static entry wins over an announced one of the same name, a read that fails leaves the nodes as they were, and a SIGHUP reads the registry file again and merges again. A SWARM-SYNC that is not on this machine is read over https unless `allowPlaintext` is set on purpose. *Open on the side of SWARM-SYNC:* the keys and the value above are this project's proposal, no node writes them yet.

**Fencing (mandatory: `-fencing-config FILE`, see `fencing.example.json`, applies with `-orchestrator-url`).** Without the file, or with a file that does not say `"enabled"`, fencing is on: with no file at all every recovery is refused (logged and counted) and nothing is requeued. It can be turned off only on purpose with `"enabled": false` in the file (optional `"disabledReason"`, written to the log at start-up and shown on `/metrics` as `hydra_node_healing_fencing_info`; `hydra_node_healing_fencing_enforced` is 1 or 0). The watchdog does not see which missions of a node are in progress, so it asks before **every** recovery request, and a mission that is in progress is never requeued without a confirmation. Two modes: `confirm` asks the cell to hold the robots (`POST holdUrl`, optional; `{node}` is replaced by the dispatch node, escaped) and reads `GET statusUrl` (`{"state": "..."}`) every `pollMillis` until the state is one of `safeStates` (stopped, held or isolated by default) or `confirmTimeoutSeconds` pass; `lease` accepts only when the node has been failing for longer than `leaseTtlSeconds + marginSeconds`, trusting the robot to stop by itself when its lease is not renewed. Everything else is a refusal: no answer, an error status, a body that is not understood, another state, a timeout, a context that ended, a cell that is not on this machine reached over plain http. A refusal logs `recovery for X NOT requested`, is counted (`hydra_node_healing_fence_refused_total`; `..._fence_confirmed_total` counts the others), and the background retry asks again on its bounded schedule; when it gives up, nothing was requeued. *Open on the side of the cell:* the two routes and the states are named in the file because HYDRA-UMC-SERVER and the safety zones do not define them yet (the values of `fencing.example.json` are illustrative).

**Watch stream (optional, off by default: `"watch": true` in a registry entry that has an `address`).** For such a node the watchdog opens the server-streaming method `/hydra.common.v1.HealthService/Watch` (request `Empty`, replies `HealthReport`) and classifies every report like the answer of `Check()`. While a stream has reported inside the last 20 seconds the poll of that node is skipped; when it has been quiet longer, it broke or the node answers `UNIMPLEMENTED` (looked at again every five minutes), the poll works as before, and a stream that breaks is followed at once by a check. The vendored stubs (`src/healthpb`) declare `rpc Watch(Empty) returns (stream HealthReport)` exactly as `hydra_common.proto` of HYDRA-UMC-ORCHESTRATOR does, and the watchdog follows the typed stream. *Served today:* HYDRA-UMC-ORCHESTRATOR and HYDRA-UMC-JOB-DISPATCHER serve `Watch()` (off by default on their side; see `docs/HEALTH_GRPC.md` of each). To follow them, add an entry to the registry whose `name` is exactly the name the service reports as its identity (`HYDRA-UMC-ORCHESTRATOR`, `HYDRA-UMC-JOB-DISPATCHER`; another name makes the node `INVALID`) with `"watch": true` and the loopback `address`: `127.0.0.1:50114` for the orchestrator and `127.0.0.1:50090` for the job dispatcher (`nodes.example.json` has both). By decision the supervision goes over loopback only, with no token and no TLS: the watchdog dials these addresses without credentials, so the services have to run on the same machine as the watchdog.
