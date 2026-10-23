<p align="center">
  <img src="images/HYDRA_UMC_BANNER.svg" alt="HYDRA-UMC-NODE-HEALING banner" width="100%">
</p>

# 💊 HYDRA-UMC-NODE-HEALING

<p align="center">🇺🇸 <b>English</b> | <a href="README_spa.md">🇪🇸 Español</a> | <a href="README_fra.md">🇫🇷 Français</a> | <a href="README_ita.md">🇮🇹 Italiano</a> | <a href="README_deu.md">🇩🇪 Deutsch</a> | <a href="README_zho.md">🇨🇳 简体中文</a> | <a href="README_jpn.md">🇯🇵 日本語</a> | <a href="README_por.md">🇵🇹 Português</a></p>

### 🛡️ High-Availability Monitor & Failover Manager for HydraNodes

<p align="center">
  <img src="https://img.shields.io/badge/License-GPL%203.0--or--later-blue.svg" alt="GPL 3.0 or later">
  <img src="https://img.shields.io/badge/Feature-Self--Healing-green.svg" alt="Self-Healing">
  <img src="https://img.shields.io/badge/Platform-Distributed%20Edge-blue.svg" alt="Platform">
</p>

---

**Honesty check - what actually runs today:** the polling/classification loop (`src/watchdog/watchdog.go`), the bounded retry policy (`src/watchdog/retry.go`), the real recovery-request wiring to HYDRA-UMC-ORCHESTRATOR (`src/watchdog/orchestrator_reactor.go`), and the static node-registry loader (`src/config/config.go`) are all real and tested (78 tests passing across `src/config` and `src/watchdog`) - and these are real gRPC round-trips over real loopback sockets, not a mocked client. What is a known, documented gap: the node registry is a static JSON file (`nodes.json`), not a live query to HYDRA-UMC-SWARM-SYNC, because that project has no real API yet either to query - this is the honest v0, not a placeholder pretending to be dynamic. Rerouting the actual in-flight mission work away from a failed node is also not this repository's job - it requests recovery through `OrchestratorReactor` and stops there; the next layer up (actually moving jobs) lives elsewhere. The 4 roadmap phases (digital-twin sync, Isaac Sim physics integration, automated recovery patterns, AI-driven predictive healing) are aspirational future work with no code behind them yet. See `CHANGELOG.md` for exactly what has shipped so far.

---

> **Safety and security status:** this ecosystem is not a certified safety system. There is no hardware E-STOP path, no firmware-enforced limits or interlocks and no verified motion command yet; firmware images are checked with CRC32 and a shared-key HMAC-SHA256, not with an asymmetric signature; TLS and mutual TLS are optional and off by default; no custom board has been populated or tested yet. Read the [security baseline](https://github.com/JuanenRac/HYDRA-UMC-DOCS/blob/main/docs/SECURITY_BASELINE.md) and the [threat model](https://github.com/JuanenRac/HYDRA-UMC-DOCS/blob/main/docs/THREAT_MODEL.md) before relying on any statement here for the safety of a person.

## 📋 At a glance

| | |
|---|---|
| Version | `0.1.8` |
| Maturity | Functional |
| Tests | 98 |
| Stack | Go / gRPC health checks / HTTP health probes |
| Target | The Raspberry Pi CM5 of a HYDRA-UMC cell |
| On real hardware | Not yet: no run on real hardware is recorded for this version |

## 1. 🛠️ TECHNICAL OVERVIEW

**HYDRA-UMC-NODE-HEALING** is the resilience layer of the swarm. It continuously monitors the health of all physical HydraNodes (Controllers) and logical services, ensuring zero downtime in the micro-factory.

If a node fails due to hardware malfunction or network outage, the Healing Manager automatically triggers a failover process, redirecting its active missions to other nodes and notifying the operator via the Orchestrator.

### Key Features:
* 💓 **Health Heartbeat:** every node is polled every 5 seconds (gRPC HealthService or HTTP health URL) and classified HEALTHY, DEGRADED, UNHEALTHY, UNREACHABLE, INVALID or MISCONFIGURED; no thermal data is read.
* 🔄 **Guarded Failover:** asks the Orchestrator to requeue the missions of a node that STAYS down (confirmation wait, start-up grace, per-node cooldown and a global budget), only for registry entries that name their `dispatchNode`.
* 🛡️ **Soft Reboot:** *(planned)* the watchdog never restarts anything today; it only reports and asks the Orchestrator to requeue.
* 📡 **Operator Alerts:** *(planned)* today every transition goes to the journal only.
* 🔁 **Bounded Retry + Verified Node Identity (v0, real):** a transient network blip no longer flips a node to `UNREACHABLE` on the first miss - `checkNode` retries with a deterministic, capped exponential backoff before giving up; a node that answers but cannot correctly self-identify is classified `INVALID` and never trusted, retried, or reported healthy.
* 📓 **Persistent journal (v0, real):** with `-journal PATH` every transition, recovery request and held-back request is also written as one JSON line, each chained to the previous by its SHA-256 and rotated by size (`-journal-max-bytes`, `-journal-backups`), so after a restart there is still a record of why a node was restarted. `-journal-verify` checks the chain. Off by default; a line that cannot be written is reported and never stops the watchdog. The systemd unit starts with the journal off; turn it on by putting `HYDRA_UMC_NODE_HEALING_JOURNAL_ARGS=--journal /var/lib/hydra-umc-node-healing/journal.jsonl` in `/etc/hydra-umc-node-healing/journal.env` (the OS installer's `--journal` option writes it). Whatever reaches the journal or a probe detail passes through the shared secret redaction. `hydra-umc-node-healing -nodes-from-ports PATH/contracts/ports.yaml` prints a node registry built from the service registry of the ecosystem (every service with a health path, probed over HTTP) and lists the services it had to leave out, so `nodes.json` need not be typed by hand.

---

## 2. 🔄 HEALING WORKFLOW

```mermaid
flowchart TB
    MONITOR["Swarm Health Monitor"] -- Heartbeat --> N1["Node 1"]
    MONITOR -- Heartbeat --> N2["Node 2"]
    N1 -- Timeout/Error --> DETECT["Failure Detected"]
    DETECT --> DIAG["Diagnostic Engine"]
    DIAG -- Critical --> FAIL["FAILOVER: Move Jobs to Node 2"]
    DIAG -- Recoverable --> RESET["SOFT-REBOOT: Restart Service"]
    FAIL --> ORCH["HYDRA-ORCHESTRATOR"]
```

---

## 3. 🧱 ARCHITECTURE & DESIGN DECISIONS

* **Why the real logic lives under `src/`, not at the repo root.** `src/healthpb` (generated gRPC stubs), `src/watchdog` (the polling engine) and `src/config` (the node registry loader) hold the actual implementation; `main.go`/`version.go` stay at the repo root as the entry point that wires them together.
* **Why detection is separate from the orchestrator it protects.** A node-healing watchdog that ran INSIDE the orchestrator process couldn't detect that same process hanging - running as an independent service is what makes 'detect an unresponsive node and reroute its work' actually possible, including when the unresponsive node is the orchestrator itself.
* **Detection AND real failover are both wired now.** `src/watchdog` polls every registered node's `HealthService.Check()` (the shared `hydra.common.v1` gRPC contract from `HYDRA-UMC-ORCHESTRATOR/proto/hydra_common.proto`) on a real interval over a real network connection, classifies HEALTHY/DEGRADED/UNHEALTHY/UNREACHABLE, and fires a `Reactor` callback on every state *change* (never every tick). `OrchestratorReactor` (`src/watchdog/orchestrator_reactor.go`) is the real `Reactor` implementation used in production: it calls HYDRA-UMC-ORCHESTRATOR's own `POST /nodes/:node/recover` on a transition into an unhealthy state, requesting a real recovery action rather than just logging the detection.
* **Why the node registry is a static JSON file, not a live query to HYDRA-UMC-SWARM-SYNC.** SWARM-SYNC (the README's original stated source of truth for "every cell in the swarm") has no real API yet either - it's still andamiaje. A static `nodes.json` (see `nodes.example.json`) is the honest v0, not a placeholder pretending to be dynamic. Swap `src/config.LoadNodes` for a real SWARM-SYNC client once that project has one to call.
* 🩺 **Probes the services that exist, over HTTP, and sends its token (v0):** an entry in `nodes.json` can carry a `healthUrl` (`http://…` or `https://…`) instead of a gRPC `address`: the watchdog sends one `GET` and classifies the answer — `2xx` healthy (a JSON `status` of `degraded`/`unhealthy` is honoured, and a JSON `name` that differs from the registry name is `INVALID`), `5xx` unhealthy, no answer `UNREACHABLE` (retried), a redirect or a `4xx` `INVALID` (a wrong URL or a missing credential is configuration, not a node failure, so it is never retried, and redirects are never followed). Until now every node was probed over a gRPC `HealthService` that no service of the ecosystem implements, so a perfectly healthy service was reported `UNREACHABLE`. The recovery request to the orchestrator carries `Authorization: Bearer` when `-orchestrator-token` / `ORCHESTRATOR_TOKEN` is set (an orchestrator on a network address refuses it otherwise), and the node name is escaped as one path segment. *(implemented)*
* **How this fits the rest of the ecosystem.** A sibling service under HYDRA-UMC-ORCHESTRATOR - watches every node in its registry, reports state changes, and now requests real recovery through `OrchestratorReactor` when one goes unhealthy; rerouting the actual in-flight work away from that node is the next layer above this.
* **Why a transport failure is retried (bounded) but an identity mismatch never is.** A connection refused or an RPC timeout can be a genuinely transient blip - a node mid-restart, a brief network hiccup - so `checkNode` retries it up to `RetryPolicy.MaxAttempts` times with exponential backoff before giving up. A node that answers but reports the wrong name (or none at all) is a different kind of problem entirely: no amount of waiting fixes a service bound to the wrong port, so that case is classified `StatusInvalid` immediately, with zero retries.
* **Why the backoff has no random jitter.** A real production fleet would want jitter to avoid a thundering herd of simultaneous reconnects, but this watchdog already polls each node from its own goroutine at its own pace - the only thing jitter would cost here is making `RetryPolicy.Backoff()` non-deterministic and harder to assert on in tests. Add jitter if/when this watchdog ever polls hundreds of nodes against a shared bottleneck resource.

For the full picture, see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the architecture guide, [`docs/BUILD_AND_RUN.md`](docs/BUILD_AND_RUN.md) for the release-vs-test build flow, and [`docs/INTEGRATION_CONTRACT.md`](docs/INTEGRATION_CONTRACT.md) for the versioned health-snapshot contract a future adapter must honor.

---

## 📂 DIRECTORY STRUCTURE

```text
HYDRA-UMC-NODE-HEALING/
├── src/
│   ├── healthpb/      # Generated Go stubs for hydra.common.v1
│   │                  # (vendored from HYDRA-UMC-ORCHESTRATOR/proto/
│   │                  # hydra_common.proto - see that repo's proto/README.md
│   │                  # for the codegen command)
│   ├── watchdog/      # Real polling loop: dial, Check(), classify, react
│   │                  # on state change only, plus retry.go (RetryPolicy)
│   └── config/        # Static JSON node registry loader
├── build/             # Compiled binaries (build.sh/build.bat output)
├── images/            # Media and diagrams
├── systemd/
│   └── hydra-umc-node-healing.service # Local CM5 watchdog systemd unit
├── tools/
│   ├── build_test.py  # Non-versioning build/compile check
│   └── ci_validate.py # Manifest/CHANGELOG/docs validation used by CI
├── nodes.example.json # Example node registry (see src/config)
├── go.mod / go.sum    # Go module definition
├── version.go         # const Version = "X.Y.Z" (go.mod has no app version field)
├── main.go            # Entry point: loads the registry, runs the watchdog
├── bump_version.py    # Odometer-style version bump (run only by the explicit `bump` command)
├── bump_manifest_version.py # Syncs hydra-umc.project.json's version to the native one (--sync)
├── build.sh/.bat      # Validate and build (building never changes the version)
├── build-test.sh/.bat # Non-versioning build check (no CHANGELOG/version bump)
├── run.sh/.bat        # Runs the compiled binary
├── docs/
│   ├── ARCHITECTURE.md
│   ├── BUILD_AND_RUN.md
│   └── INTEGRATION_CONTRACT.md
└── README.md
```

Pruned from the original template: `hardware/`, `firmware/`, `os/` and `scripts/` — this is a pure software service (Go binary) with no dedicated hardware or firmware of its own, no operating system image to maintain and no utility-script content yet that would warrant a folder of its own.

---

## 🔧 BUILD & RUN GUIDE

A real watchdog, not just a skeleton that compiles: it dials every node in
`nodes.example.json` (or `-nodes <path>` to point at your own registry)
over gRPC and reports state changes to stdout.

```bash
# Windows
build.bat
run.bat -nodes nodes.example.json

# Linux / macOS
./build.sh
./run.sh -nodes nodes.example.json
```

`build.sh`/`build.bat` never change the version (only `python tools/hydra_project_tool.py bump` raises it) and then run `go build`. `run.sh`/`run.bat`
execute the resulting binary directly.

An entry with a `healthUrl` (`http://…`, the way to watch the services of
this ecosystem) needs that URL to answer `2xx`; an entry with an `address`
needs something real listening there and implementing
`hydra.common.v1.HealthService` (see
`HYDRA-UMC-ORCHESTRATOR/proto/hydra_common.proto`) to ever report
healthy - with nothing running yet on any of the example ports, expect
every node to print `UNKNOWN -> UNREACHABLE` on the first tick (now only
after `RetryPolicy` exhausts its bounded attempts, not on the very first
dial - see the "Bounded Retry" feature above), which is the correct,
honest result today (every node in this ecosystem is still andamiaje
beyond this repo's own detection logic). A node that answers but cannot
correctly self-identify prints `UNKNOWN -> INVALID` instead, immediately.

```bash
go test ./...   # src/config + src/watchdog, real gRPC round-trips
                 # over real loopback sockets, no mocked client
```

---

## 🚀 ROADMAP
* **Phase 1:** (done, `-journal`) A persistent, hash-chained JSON-lines journal of every transition and recovery request. Still to come: the decisions of the Orchestrator's answers as structured fields.
* **Phase 2:** A faster probe: one loop per node with persistent gRPC connections, and headers per entry for the HTTP probe.
* **Phase 3:** Its own observability (a metrics endpoint) and a reload of the node registry without a restart.
* **Phase 4:** A contract test against a real HYDRA-UMC-SERVER and a lease so that only one healer acts at a time.

---

## 🔗 Related Projects

This project is part of the HYDRA-UMC robotics ecosystem by the same author (JuanenRac / Electro Hobby 3D). Worth knowing about, since a request might actually be about one of these rather than this repository.

**Parent Project**
- **[HYDRA-UMC-ORCHESTRATOR](https://github.com/JuanenRac/HYDRA-UMC-ORCHESTRATOR)** — integration hub with a real gRPC/Protobuf health-report contract and mission state machine; the parent this repo is one specific orchestration service of, within its own swarm-coordination layer.

**Sibling Projects** — the other orchestration services of HYDRA-UMC-ORCHESTRATOR's own swarm-coordination layer
- **[HYDRA-UMC-SWARM-SYNC](https://github.com/JuanenRac/HYDRA-UMC-SWARM-SYNC)** — real CRDT LWW-Element-Map state sync, property-tested for multi-cell convergence.
- **[HYDRA-UMC-PATH-PLANNER-3D](https://github.com/JuanenRac/HYDRA-UMC-PATH-PLANNER-3D)** — real RRT-based 3D path planner with real obstacle/workspace collision validation.
- **[HYDRA-UMC-JOB-DISPATCHER](https://github.com/JuanenRac/HYDRA-UMC-JOB-DISPATCHER)** — real priority-based job queue with deduplication, over a real HTTP API.

**Directly Related**
- **[HYDRA-UMC-SERVER](https://github.com/JuanenRac/HYDRA-UMC-SERVER)** — the real headless backend (REST/WebSocket) every control client actually talks to — this healing service can watch its health endpoint like any other node of its registry (the example registry watches ORCHESTRATOR, VOICE-UI, DATALAKE, ANOMALY-DETECTOR and COGNITIVE-NODE, not the server).

**Also Part of the Ecosystem**

*Core Hardware & Platform*
- **[HYDRA-UMC](https://github.com/JuanenRac/HYDRA-UMC)** — the robot-arm motherboard design: CM5 host + dual-core STM32H745, meant to orchestrate up to 8 tool arms over CAN-OTA/SPI-OTA (firmware at an early stage; no populated board yet).
- **[HYDRA-UMC-OS](https://github.com/JuanenRac/HYDRA-UMC-OS)** — reproducible Raspberry Pi OS product layer for the CM5: read-only agent, validated config/profiles, WiFi first-contact provisioning.
- **[HYDRA-UMC-SDK](https://github.com/JuanenRac/HYDRA-UMC-SDK)** — the shared JSON-Schema contract and software job-gate boundary every bridge validates its commands against (not a certified safety function).
- **[HYDRA-UMC-CONNECTOR-HUB](https://github.com/JuanenRac/HYDRA-UMC-CONNECTOR-HUB)** — declarative adapter-manifest registry and validator for external-machine connectors; extends the SDK's own contract idea to external machines without replacing the industrial-gateway projects.

*Core Backend & Clients*
- **[HYDRA-UMC-STUDIO](https://github.com/JuanenRac/HYDRA-UMC-STUDIO)** — web control dashboard with real-time multi-robot 3D visualization.
- **[HYDRA-UMC-SUITE](https://github.com/JuanenRac/HYDRA-UMC-SUITE)** — desktop (PySide6) swarm command center for multiple servers at once, packaged as a standalone executable.
- **[HYDRA-UMC-ANDROID-CONTROL](https://github.com/JuanenRac/HYDRA-UMC-ANDROID-CONTROL)** — native Android control app with biometric login and a paired Wear OS companion.
- **[HYDRA-UMC-IOS-CONTROL](https://github.com/JuanenRac/HYDRA-UMC-IOS-CONTROL)** — iOS/iPadOS control app (Flutter) with real-time WebSocket sync.
- **[HYDRA-UMC-DSI](https://github.com/JuanenRac/HYDRA-UMC-DSI)** — native touch UI for the onboard 7" DSI touchscreen, embedded on the CM5 itself.
- **[HYDRA-UMC-EDITOR-URDF](https://github.com/JuanenRac/HYDRA-UMC-EDITOR-URDF)** — desktop graphical URDF creator/editor that pushes finished models into STUDIO's own catalog.
- **[HYDRA-UMC-EDITOR-STL](https://github.com/JuanenRac/HYDRA-UMC-EDITOR-STL)** — desktop STL model editor, transforming/replacing/removing/adding real parts in the same model catalog HYDRA-UMC-EDITOR-URDF also edits.
- **[HYDRA-UMC-BRIDGE-AMR](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-AMR)** — coordination boundary for AGV/AMR fleets via a VDA 5050 MQTT publisher (tested against an emulated fleet only).
- **[HYDRA-UMC-BRIDGE-CNC](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-CNC)** — high-level CNC-cell coordinator with GRBL status/control-byte access (tested against an emulator only).
- **[HYDRA-UMC-BRIDGE-DROIDS](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-DROIDS)** — coordination boundary for legged/humanoid droids, with a Boston Dynamics Spot command sender (tested against an emulator only).
- **[HYDRA-UMC-BRIDGE-LASER](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-LASER)** — laser-cell coordinator reading 3 key/enclosure/interlock GPIO signals (tested against a fake chip only; it cannot arm or fire a laser).
- **[HYDRA-UMC-BRIDGE-OPENPNP](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-OPENPNP)** — safe high-level board-flow coordinator for OpenPnP pick-and-place.
- **[HYDRA-UMC-BRIDGE-PRINTER3D](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-PRINTER3D)** — safe coordination boundary for Moonraker/Klipper 3D printers, with real gated job commands.
- **[HYDRA-UMC-BRIDGE-ROS2](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-ROS2)** — coordinator with a lazily imported rclpy ROS 2 transport (not yet run against a ROS 2 installation).
- **[HYDRA-UMC-BRIDGE-UAV](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-UAV)** — coordination boundary for camera-equipped UAVs, with a MAVLink command sender (tested against an emulator only).

*URTC Tool Platform*
- **[URTC](https://github.com/JuanenRac/URTC)** — firmware for the physical Universal Robot Tool Controller PCB, 25+ tool profiles over CAN bus.
- **[URTC-FLASHER](https://github.com/JuanenRac/URTC-FLASHER)** — desktop GUI flashing tool for URTC boards, CAN-OTA plus full-chip SWD/JTAG.
- **[URTC-TESTER](https://github.com/JuanenRac/URTC-TESTER)** — desktop live CAN-bus diagnostic tool for URTC boards, one panel per tool profile.
- **[URTC-WEB-STUDIO](https://github.com/JuanenRac/URTC-WEB-STUDIO)** — browser-based alternative to URTC-TESTER via the Web Serial API, no local install needed.
- **[URTC-GATEWAY](https://github.com/JuanenRac/URTC-GATEWAY)** — gateway in front of the URTC CAN bus: an allowed list of identifiers, a rate limit and a session key; software tested on a computer, not yet on a real bus.

*Vision AI Node (Hailo-8)*
- **[HYDRA-UMC-VISION-NODE](https://github.com/JuanenRac/HYDRA-UMC-VISION-NODE)** — integration hub for the Hailo-8 vision pipeline, with a real per-stage hardware-readiness check.
- **[HYDRA-UMC-DETECTION-HEF](https://github.com/JuanenRac/HYDRA-UMC-DETECTION-HEF)** — real compiled-model registry with Hailo-architecture/checksum safe-load verification.
- **[HYDRA-UMC-VISION-STREAMER](https://github.com/JuanenRac/HYDRA-UMC-VISION-STREAMER)** — real GStreamer pipeline + MediaMTX config generator with a real HailoRT integration boundary.
- **[HYDRA-UMC-VISUAL-SERVOING-API](https://github.com/JuanenRac/HYDRA-UMC-VISUAL-SERVOING-API)** — Position-Based Visual Servoing correction law with authorisation gated on the zone state; no camera pose estimation yet.
- **[HYDRA-UMC-SAFETY-ZONES](https://github.com/JuanenRac/HYDRA-UMC-SAFETY-ZONES)** — v0 zone-breach, calibration-freshness and E-STOP-request logic that takes object positions from a file; no detector feeds it and no physical E-STOP is wired to it yet.

*Cognitive AI Node (Hailo-10)*
- **[HYDRA-UMC-COGNITIVE-NODE](https://github.com/JuanenRac/HYDRA-UMC-COGNITIVE-NODE)** — integration hub for the Hailo-10 cognitive pipeline (LLM/VLA/voice orchestration).
- **[HYDRA-UMC-VLA-ENGINE](https://github.com/JuanenRac/HYDRA-UMC-VLA-ENGINE)** — real action-token encoding/decoding and trajectory generation for a Vision-Language-Action model.
- **[HYDRA-UMC-VOICE-UI](https://github.com/JuanenRac/HYDRA-UMC-VOICE-UI)** — real voice front-end (VAD + intent parser) with a bounded, confirmation-gated Watch relay.
- **[HYDRA-UMC-SEMANTIC-PLANNER](https://github.com/JuanenRac/HYDRA-UMC-SEMANTIC-PLANNER)** — real rule-based task decomposition and semantic error recovery over MCU error codes.
- **[HYDRA-UMC-DOCS-QA](https://github.com/JuanenRac/HYDRA-UMC-DOCS-QA)** — real stdlib-only TF-IDF document search over this ecosystem's own Markdown docs.
- **[HYDRA-UMC-LOCAL-TECHNICIAN](https://github.com/JuanenRac/HYDRA-UMC-LOCAL-TECHNICIAN)** — local, policy-gated AI maintenance technician for the ecosystem itself - observes, diagnoses and proposes fixes; the two highest risk levels are deliberately not implemented yet.

*Digital Twin & Simulation*
- **[HYDRA-UMC-TWIN](https://github.com/JuanenRac/HYDRA-UMC-TWIN)** — integration hub for the digital-twin engine, with a real version-compatibility sync contract.
- **[HYDRA-UMC-HIL-BRIDGE](https://github.com/JuanenRac/HYDRA-UMC-HIL-BRIDGE)** — hardware-in-the-loop router with a software collision interlock between simulation and real hardware (tested without real hardware).
- **[HYDRA-UMC-PHYSICS-REPLICA](https://github.com/JuanenRac/HYDRA-UMC-PHYSICS-REPLICA)** — real forward kinematics and joint-limit validation over a real URDF subset.
- **[HYDRA-UMC-SYNTHETIC-DATA-GEN](https://github.com/JuanenRac/HYDRA-UMC-SYNTHETIC-DATA-GEN)** — real procedural 2D scene generator with YOLO/COCO annotation export.

*Data & Analytics*
- **[HYDRA-UMC-DATALAKE](https://github.com/JuanenRac/HYDRA-UMC-DATALAKE)** — real sqlite3-backed time-series store with a real ingest/query HTTP API.
- **[HYDRA-UMC-ANOMALY-DETECTOR](https://github.com/JuanenRac/HYDRA-UMC-ANOMALY-DETECTOR)** — real FFT + statistical baseline anomaly detector with drift monitoring.
- **[HYDRA-UMC-PRODUCTION-REPORTS](https://github.com/JuanenRac/HYDRA-UMC-PRODUCTION-REPORTS)** — real OEE/availability calculation over DATALAKE history, with reproducible CSV export.
- **[HYDRA-UMC-TELEMETRY-COLLECTOR](https://github.com/JuanenRac/HYDRA-UMC-TELEMETRY-COLLECTOR)** — real CAN/WebSocket ingestion pipeline into DATALAKE, with sequence deduplication.

*Industrial Gateway*
- **[HYDRA-UMC-GATEWAY-INDUSTRIAL](https://github.com/JuanenRac/HYDRA-UMC-GATEWAY-INDUSTRIAL)** — integration hub relaying to industrial protocols, with a real command allowlist/backpressure layer.
- **[HYDRA-UMC-OPCUA-SERVER](https://github.com/JuanenRac/HYDRA-UMC-OPCUA-SERVER)** — real OPC-UA address space, verified with a real binary-protocol client session.
- **[HYDRA-UMC-MQTT-BROKER](https://github.com/JuanenRac/HYDRA-UMC-MQTT-BROKER)** — real MQTT broker with optional per-client authentication and topic ACLs.
- **[HYDRA-UMC-MTCONNECT-ADAPTER](https://github.com/JuanenRac/HYDRA-UMC-MTCONNECT-ADAPTER)** — real MTConnect `/probe` and `/current` XML endpoints with degraded-mode output.

*Complementary Tools & Ecosystem Operations*
- **[HYDRA-UMC-DASHBOARD-AI](https://github.com/JuanenRac/HYDRA-UMC-DASHBOARD-AI)** — Smart Summaries and Anomaly Highlighting panels over DATALAKE/ANOMALY-DETECTOR, with an honest statistical fallback.
- **[HYDRA-UMC-TOOL-CLI](https://github.com/JuanenRac/HYDRA-UMC-TOOL-CLI)** — fleet CLI with a real, stable exit-code contract, a genuine live client of HYDRA-UMC-SERVER's own API.
- **[HYDRA-UMC-WATCH](https://github.com/JuanenRac/HYDRA-UMC-WATCH)** — WearOS companion app with haptic alert patterns and a paired-phone voice relay (not yet run on a physical watch).
- **[URTC-SMART-RACK](https://github.com/JuanenRac/URTC-SMART-RACK)** — firmware for a board-mounting rack with real tool-ID decoding and Smart Idle pre-heating logic.
- **[URTC-VISION-TOOL](https://github.com/JuanenRac/URTC-VISION-TOOL)** — firmware plus a real Python vision companion for a thermal/RGB inspection tool head.
- **[HYDRA-UMC-UPDATER](https://github.com/JuanenRac/HYDRA-UMC-UPDATER)** — administrative desktop tool that discovers, clones and updates every repo in this ecosystem.
- **[HYDRA-UMC-OS-REBUILDER](https://github.com/JuanenRac/HYDRA-UMC-OS-REBUILDER)** — Windows/Linux desktop tool that builds a ready-to-flash CM5 image pre-loaded with the ecosystem's most current versions, with Raspberry-Pi-Imager-style first-boot Wi-Fi/user/SSH configuration.
- **[HYDRA-UMC-OPS-AGENT](https://github.com/JuanenRac/HYDRA-UMC-OPS-AGENT)** — maintenance-incident coordinator: a low-privilege edge role collects a sanitized inventory/health snapshot, a control-plane role renders it read-only and asks an AI provider to suggest a diagnosis - never applies a patch or deploys anything.
- **[HYDRA-UMC-DEV-SERVER](https://github.com/JuanenRac/HYDRA-UMC-DEV-SERVER)** — reproducible development host (Raspberry Pi 5 / CM5) that stores the ecosystem's source and runs bounded build/test tasks under a durable queue; a dedicated dev role, explicitly not an operational CM5.


---

## 📚 Documentation & Community

- **[CONTRIBUTING.md](CONTRIBUTING.md)** — tech stack and coding guidelines for a pull request.
- **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)** — the standards of behavior expected in this community.
- **[SECURITY.md](SECURITY.md)** — how to report a vulnerability, and this project's own real security focus areas.
- **[SUPPORT.md](SUPPORT.md)** — where to ask questions and report bugs.
- **[LICENSE.md](LICENSE.md)** — this project's own license.

## 👤 AUTHOR
**JuanenRac** (Electro Hobby 3D)
📧 electrohobby3d@gmail.com
📺 [youtube.com/@electrohobby3d](https://youtube.com/@electrohobby3d)

## 📜 LICENSE
GPL-3.0-or-later - See LICENSE for details.
