# Contributing to HYDRA-UMC-NODE-HEALING 🦾

We welcome contributions to the resilience and failover manager of the HYDRA-UMC platform.

## Technology Stack
- **Language**: Go 1.25+ (pure Go, no cgo) — see `go.mod`.
- **Monitoring**: real polling over the shared `hydra.common.v1` gRPC contract (`HealthService.Check()`, vendored from `HYDRA-UMC-ORCHESTRATOR/proto/hydra_common.proto`).
- **Node registry**: static `nodes.json` loaded by `src/config`, see `nodes.example.json`.
- **Architecture**: single watchdog loop (`src/watchdog`) classifying HEALTHY/DEGRADED/UNHEALTHY/UNREACHABLE/INVALID, with bounded retry/backoff (`RetryPolicy`) and a `Reactor` callback fired only on a real state change.

## Guidelines
1. **Low Overhead**: The health monitor must use minimal CPU/Network resources to avoid impacting real-time control.
2. **Failover Determinism**: Any future failover/recovery logic must be idempotent and deterministic to prevent mission duplication.
3. **Retry vs. Identity**: A transport failure (dial/RPC error) may be retried, bounded by `RetryPolicy`; a node that answers but misreports its own identity is classified `INVALID` immediately and never retried — see `README.md`'s Architecture & Design Decisions.
4. **Testing**: `go test ./...` covers `src/config` and `src/watchdog` with real gRPC round-trips over real loopback sockets — add tests in that same style rather than mocking the transport.
