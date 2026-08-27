# Changelog

All notable work on **HYDRA-UMC-NODE-HEALING** is summarized here, newest first. Full
session-by-session detail (including dates) lives in a private,
unpublished internal log - this file is public, so it intentionally
omits calendar dates.

## Versioning scheme

`version.go`'s `Version` constant is bumped automatically by
`bump_version.py`, run from `build.sh`/`build.bat` before every real
release build (`go build`).

It follows the ecosystem-wide base-10 "odometer" rule rather than
semantic-versioning judgment calls:

- `PATCH` +1 on every build
- when `PATCH` would exceed 9, it resets to 0 and `MINOR` +1 instead (e.g. `0.0.9` -> `0.1.0`, never `0.0.10`)
- the same carry cascades into `MAJOR` if `MINOR` would exceed 9

---

## [0.0.3] - Source layout: `src/` instead of `internal/`, unused folders removed

- Moved `internal/healthpb`, `internal/watchdog` and `internal/config`
  (added in 0.0.2) to `src/healthpb`, `src/watchdog` and `src/config` -
  this repo's real source now lives where the README always said it
  would (`src/`), rather than introducing a second, competing location.
  `main.go`/`version.go` stay at the repo root as the entry point.
- Removed the empty, unused `docs/`, `images/` and `scripts/` folders
  (moved to `SONNET/_papelera/`, never deleted outright) - kept only
  while there was nothing real to put in them; real content can recreate
  them later.
- `run.sh`/`run.bat` now forward CLI arguments (`"$@"`/`%*`) to the
  compiled binary - a gap from 0.0.2 (the README already showed
  `run.bat -nodes ...`, but the script silently dropped the flag).
- `build.sh`/`build.bat` and `run.sh`/`run.bat` no longer close their
  window immediately on exit: `build`/`run.bat` now `pause` (including
  on a failed build), and `build`/`run.sh` set an `EXIT` trap that
  prompts before closing - but only when stdin is actually a terminal
  (`[ -t 0 ]`), so CI/piped/non-interactive runs are unaffected.
- Verified for real: `go build ./...`, `go vet ./...` and `go test ./...`
  all clean after the move (import paths updated accordingly); `build.sh`
  run for real end-to-end (version bump + compile) after the trap change,
  confirmed it does not hang non-interactively.

## [0.0.2] - Real detection: node polling over the shared gRPC health contract

- **`src/healthpb/`** - Go stubs generated from the ecosystem-wide
  `hydra.common.v1` contract (`HYDRA-UMC-ORCHESTRATOR/proto/hydra_common.proto`),
  vendored locally rather than pulled as a shared Go module dependency
  between repos (each consumer generates its own bindings - see that
  proto's own `proto/README.md`).
- **`src/watchdog`** - the real polling loop: dials every registered
  node over gRPC, calls `HealthService.Check()`, classifies the result as
  `HEALTHY` / `DEGRADED` / `UNHEALTHY` / `UNREACHABLE` (the last one is
  this watchdog's own addition - a node cannot self-report that it is
  unreachable), and fires a `Reactor` callback only on an actual state
  *change*, never once per poll. `watchdog.LogReactor` (the default) logs
  every transition; `watchdog.Reactor` is the seam for a future
  ORCHESTRATOR-backed failover/soft-reboot implementation once that API
  exists.
- **`src/config`** - static JSON node registry loader
  (`nodes.example.json`), with explicit, loud errors on a missing/empty/
  malformed registry rather than silently watching nothing.
- **`main.go`** - now actually runs the watchdog loop against a
  `-nodes <path>` registry (default `nodes.example.json`) until
  SIGINT/SIGTERM, instead of only printing identity and exiting.
- Verified for real, not just "builds": `go vet ./...` clean; 8
  `go test ./...` cases pass, including 2 real gRPC round-trip tests
  (`src/watchdog`) that start an actual `net.Listen` + `*grpc.Server`
  fake node, poll it over real loopback sockets, kill it, rebind the same
  address to simulate a restart, and assert the exact transition sequence
  `UNKNOWN -> HEALTHY -> UNREACHABLE -> HEALTHY` with no duplicate
  transitions while the state doesn't change. Additionally smoke-tested
  the compiled binary itself end-to-end against a throwaway fake
  `HYDRA-UMC-ORCHESTRATOR` node on a real TCP port - correct
  `UNKNOWN -> HEALTHY` output for the running node and
  `UNKNOWN -> UNREACHABLE` for the two not-yet-implemented ones in the
  example registry.
- What's still not real: automatic failover/soft-reboot (needs an
  ORCHESTRATOR API that doesn't exist yet) and dynamic node discovery via
  HYDRA-UMC-SWARM-SYNC (same reason) - see `README.md`'s Architecture &
  Design Decisions section for why detection didn't need to wait on
  either.

## [0.0.1] - Initial scaffolding

- **`main.go`** - minimal real entry point. No healing logic yet - detecting an unresponsive/degraded node and automatically rerouting its work lands in a later pass.
- **`version.go`** - version identity (`Version` constant).
- **`build.sh` / `build.bat`**, **`run.sh` / `run.bat`** - `go build` and run the resulting binary.
