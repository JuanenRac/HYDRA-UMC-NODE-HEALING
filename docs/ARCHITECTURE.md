<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Architecture guide
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Architecture

This Go service polls every registered node's health and classifies it
HEALTHY/DEGRADED/UNHEALTHY/UNREACHABLE/INVALID. A registry entry with an
`address` is probed over gRPC (`HealthService.Check()`, the shared
`hydra.common.v1` contract); an entry with a `healthUrl` is probed with one
HTTP `GET` (`src/watchdog/httpprobe.go`) - the way to watch the services of
this ecosystem, which speak HTTP and do not implement the gRPC service. A
`Reactor` callback fires
callback on every state *change* (never every tick). `nodes.example.json` is
a local example input; production node inventory must be supplied by an
authenticated deployment adapter.

Two real `Reactor` implementations exist today, selected by the `--orchestrator-url`
flag - there is no third, hypothetical mode:

- **Observation-only (`watchdog.LogReactor`, the default, `--orchestrator-url` unset):**
  logs every transition. Takes no action of any kind.
- **Real recovery request (`watchdog.OrchestratorReactor`, `--orchestrator-url` set):**
  on a transition INTO `UNREACHABLE` or `INVALID` specifically (not every
  unhealthy transition - a `DEGRADED` node may still be doing real work), it
  sends one real `POST {orchestrator-url}/nodes/{name}/recover` (the node
  name escaped as a single path segment, `Authorization: Bearer` added when
  `-orchestrator-token` / `ORCHESTRATOR_TOKEN` is set) to HYDRA-UMC-ORCHESTRATOR,
  bounded by a 5s timeout. If that request fails,
  a bounded background retry (up to 5 attempts, 2s→30s backoff) keeps trying
  until it succeeds, the node's classification changes again, or the
  attempts run out - never blocking the watchdog's own next poll.

This is a **logical mission-recovery request**, not a physical action on the
node: it asks Orchestrator to stop trusting that node with in-flight work and
requeue it elsewhere. What actually happens to a node's mission next is
entirely Orchestrator's own decision - this repository sends the request and
stops there. It is never a restart, reflash, package change or credential
action on the node itself. The only credential it holds is the bearer
token it presents to Orchestrator, whose `/nodes/:node/recover` endpoint
asks for one on any network address. See
[`INTEGRATION_CONTRACT.md`](INTEGRATION_CONTRACT.md) for the exact contract
this sits inside, including the real limit on node-identity consistency
between this watchdog and Orchestrator's own mission-dispatch naming.
