# Changelog

All notable work on **HYDRA-UMC-NODE-HEALING** is summarized here, newest first.
This file intentionally omits calendar dates from individual entries.

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

## [Unreleased] - a hung orchestrator can no longer block this watchdog forever

- **`OrchestratorReactor`'s default HTTP client now has a real timeout**
  (`defaultRecoveryTimeout`, 5s) - found in an ecosystem-wide
  software-improvements audit: it used to fall back to
  `http.DefaultClient`, which has no timeout at all. If Orchestrator
  itself is hung - the most likely scenario during a real incident -
  the recovery POST could block forever per unhealthy-node transition,
  leaking a goroutine exactly when the system is most compromised. New
  regression test proves a server that never responds no longer blocks
  `OnTransition` past the default timeout.

## [0.1.2] - Real recovery wiring to HYDRA-UMC-ORCHESTRATOR

- **`src/watchdog/orchestrator_reactor.go`** (new) - `OrchestratorReactor`
  fills the exact seam `watchdog.go`'s own comment has named since
  `Reactor` was introduced: "swap `watchdog.LogReactor` for a real
  implementation once ORCHESTRATOR has something to call." It now does -
  `HYDRA-UMC-ORCHESTRATOR`'s own `POST /nodes/:node/recover` (real since
  that repo's own 0.0.4). Real logging happens unconditionally first
  (the same visibility `LogReactor` already provided must never regress);
  a recovery request only fires for a transition INTO
  `StatusUnreachable`/`StatusInvalid` - not `Degraded` (still alive,
  still doing real work) and not a recovery-to-`Healthy` transition
  (nothing to recover from). Honest, undisguised coupling: this
  forwards `Node.Name` as-is to Orchestrator's own node-name space -
  whoever writes `nodes.json` is responsible for keeping the two
  consistent, this file does not invent a translation between them.
- **`main.go`** - new `--orchestrator-url` flag; omitted, behavior is
  unchanged from before this existed (`LogReactor`, detection-only).
- 6 new tests (`src/watchdog/orchestrator_reactor_test.go`, real HTTP
  against `httptest.NewServer`) - 28 total.

## [0.1.1] - The 0.1.0 fix was still wrong: this binary refuses to run with zero nodes

- **`systemd/hydra-umc-node-healing.service`** - dropped the "watching
  zero nodes" framing entirely. Live-verified on the real CM5: `config.
  LoadNodes()` itself refuses an empty registry ("is empty - nothing to
  watch") and exits 1, so 0.1.0's `nodes.json` = `[]` auto-restart-looped
  exactly like the permission bug it was meant to fix, just with a
  different error. There is genuinely no real HealthService-speaking
  node anywhere in this ecosystem yet (this repo's own
  `nodes.example.json` targets 3 services that don't run one), so this
  binary correctly has nothing it can watch today. `HYDRA-UMC-OS`'s
  `install_node_healing.sh` now installs the capability only (binary +
  unit, matching `install_vision_streamer.sh`'s own pattern) and does
  NOT create a registry or enable/start the service - see that script's
  own printed instructions for what to do once a real node exists.

## [0.1.0] - Real bug found on this device's first live install: the node registry lived somewhere this service couldn't read

- **`systemd/hydra-umc-node-healing.service`** - the node registry path
  moves from `/etc/hydra-umc/node-healing/nodes.json` to
  `/etc/hydra-umc-node-healing/nodes.json`. Live-verified failure on the
  real CM5 this was first installed on: `/etc/hydra-umc/` is `0750
  root:hydra-umc-agent` (see `install_local_agent.sh` in HYDRA-UMC-OS),
  and this service's own unprivileged systemd account is in neither, so
  it could never traverse into that directory to open its own `--nodes`
  file - `permission denied` on every start, `systemd` auto-restart-
  looping forever. A plain `os.Open()` by this process is not the same
  as a `systemd` `EnvironmentFile=` directive (read by `systemd` itself,
  as root, before it drops privileges) - other services under
  `/etc/hydra-umc/` get away with the shared tree only because they never
  open a file under it themselves.

## [0.0.9] - Real CM5 deployment

- **`src/watchdog/retry.go`** - retry backoff now caps before a duration
  doubling could overflow. Extreme but valid retry-policy bounds remain
  finite and cannot become a negative delay that alters watchdog retry
  behaviour. Landed just ahead of this build, so it ships as part of
  0.0.9 rather than its own version bump.
- **`systemd/hydra-umc-node-healing.service`** (new) - unit for
  `HYDRA-UMC-OS/provisioning/install_node_healing.sh` (new, that repo),
  which builds this pure-Go binary on-device. Starts watching an
  intentionally EMPTY node registry (`/etc/hydra-umc/node-healing/nodes.json`
  = `[]`), not this repo's own `nodes.example.json` - that file lists
  HealthService gRPC endpoints for HYDRA-UMC-ORCHESTRATOR/VISION-NODE/
  COGNITIVE-NODE, none of which run as real services on this CM5 yet
  (real, separate future work). Watching zero nodes is an honest starting
  state; add real entries by hand as those nodes come online, rather than
  shipping a registry that would only ever report every node unreachable.

## [0.0.8] - New docs/ reference set

- **`docs/ARCHITECTURE.md`, `docs/BUILD_AND_RUN.md`,
  `docs/INTEGRATION_CONTRACT.md`** (new) - a real architecture overview
  (this is a decision-only health/recovery-eligibility engine, never
  itself authorized to restart/reflash/reconfigure a node), the real
  build-test-then-release flow (`build-test.sh/.bat` for the
  deterministic, non-versioning check; `build.sh/.bat` for a real
  release), and the real integration contract a future adapter must
  honor (versioned health snapshots, stable node identity, idempotent
  repeated observations, authenticated/auditable authority required for
  any real actuation). All 7 README language files' own directory tree
  updated to reference the new `docs/` folder. Documentation-only - no
  code changed. 21/21 tests still passing.

## [0.0.7] - `LoadNodes` rejects a duplicate node `name`

- **`src/config/config.go`** - found in a live ecosystem bug audit: `LoadNodes` validated an empty `name` and an empty `address` per entry but never checked a `name` against the other entries in the same registry. `Watchdog.state` (`src/watchdog/watchdog.go`) is keyed by `Node.Name` alone, so two registry entries sharing a `name` with different `address` values (a realistic copy-paste mistake when adding another instance of a node) would silently share one map slot - whichever node polled last would overwrite the other's classification, and a real failure could end up masked behind the other node's healthy status. `LoadNodes` now tracks every `name` it has already accepted and returns an error naming both the offending entry's index and the earlier entry it duplicates as soon as a repeat is seen, instead of building a registry that two different nodes silently fight over.
- New `TestLoadNodes_DuplicateName` in `src/config/config_test.go` builds a two-entry registry that shares a `name` with different `address` values and asserts `LoadNodes` returns a non-nil error naming the duplicated `name`. All existing `config` and `watchdog` tests pass unchanged (18 tests total).

## [0.0.6] - Real v0: bounded retry policy + rejection of an unverifiable node identity

- **`src/watchdog/retry.go`** (new) - `RetryPolicy{MaxAttempts, BaseDelay, MaxDelay}`, `Validate()`, and `Backoff(attempt)`: deterministic exponential backoff (no jitter, directly assertable in tests) capped at `MaxDelay`. `DefaultRetryPolicy()` (3 attempts, 250ms base, 2s cap) is bounded so a fully-exhausted retry for one node can never push a poll tick past the next one.
- **`src/watchdog/watchdog.go`** - `checkNode()` split into a retry loop (new) wrapping `attemptCheck()` (the old `checkNode` body): a transport-level failure (dial/RPC error) is retried up to `RetryPolicy.MaxAttempts` times with `Backoff()` between attempts before the node is classified `UNREACHABLE`, masking transient network blips within one poll tick without an open-ended loop.
- **`src/watchdog/watchdog.go`** - new `StatusInvalid`: a node that answers the RPC but omits `NodeIdentity` or self-identifies under a different name than the one it was registered under is classified `INVALID` and never trusted or retried - unlike a transport failure, no amount of retrying fixes a node that cannot correctly say who it is.
- `Watchdog.RetryPolicy` field (defaults to `DefaultRetryPolicy()` in `NewWatchdog`); a policy that fails `Validate()` falls back to the default rather than silently disabling retries.
- 12 new tests: `RetryPolicy.Validate()`/`Backoff()` (boundaries: `MaxDelay == BaseDelay`, `attempt <= 0`, cap reached exactly), a real gRPC integration test where a node starts mid-retry-window and the transient failure never reaches the Reactor, a bounded-time test proving retry exhaustion stays close to the sum of its own backoffs, and two real gRPC tests for `StatusInvalid` (missing identity, mismatched identity) confirming the rejection is immediate, not retried. 17 tests total.
- Fixed `build.sh`: called `bump_manifest_version.py` (no `--sync`) before `bump_version.py`, double-bumping the native version one step ahead of the manifest - reordered to match `build.bat`'s already-correct native-bump-then-sync sequence.
- Real verification beyond the test suite: ran the compiled binary against `nodes.example.json` (no real nodes running) and confirmed each entry now takes visibly longer to reach `UNREACHABLE` (the retry+backoff window), not an instant single-attempt failure.

## [0.0.5]

- Build version synchronized with `hydra-umc.project.json` and the repository-native version source.

## [0.0.4]

- Build version synchronized with `hydra-umc.project.json` and the repository-native version source.

## [0.0.3] - Source layout: `src/` instead of `internal/`, unused folders removed

- Moved `internal/healthpb`, `internal/watchdog` and `internal/config`
  (added in 0.0.2) to `src/healthpb`, `src/watchdog` and `src/config` -
  this repo's real source now lives where the README always said it
  would (`src/`), rather than introducing a second, competing location.
  `main.go`/`version.go` stay at the repo root as the entry point.
- Removed the empty, unused `docs/`, `images/` and `scripts/` folders while
  there was nothing real to put in them; real content can recreate them later.
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
