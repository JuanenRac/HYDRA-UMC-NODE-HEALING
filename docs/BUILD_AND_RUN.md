<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Build and run guide
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Build and Run

Run `build-test.bat` or `build-test.sh` first. They execute the deterministic
Go validation route without changing version or CHANGELOG. `build.bat` and
`build.sh` only build: they never change the version (`python tools/hydra_project_tool.py bump`
is the explicit command that raises it).

`run.bat` and `run.sh` are local operator entry points. Use an explicit local
node inventory and read the log of transitions it prints.

With `-orchestrator-url` the watchdog asks HYDRA-UMC-ORCHESTRATOR to requeue the work of a node that stays down, but only
for an entry that names its `dispatchNode`, and never at once: the node has to stay in its failing state for the confirmation
time, the first minute after start is a grace period, the same node is asked about at most once every few minutes and a
budget caps the requests of a flapping network (`RecoveryPolicy`). The limits are tuned with `--recover-confirm-after` (how long a node must stay down before a recovery is requested; 15 seconds by default, any change of state in between cancels it), `--startup-grace` (no recovery during the first 60 seconds after the watchdog starts), `--recover-cooldown` (at most one request per node in this time; 5 minutes) and `--recover-budget` (requests over all nodes in 10 minutes before it only logs, as a circuit breaker; 3). An answer that is only a 401 or a 404 does not count as a node that fell. With `HYDRA_UMC_ORCHESTRATOR_BIN` (or the build of the ORCHESTRATOR repository next to this one) the contract test runs against the real ORCHESTRATOR binary and is skipped when there is none.

## Watching the watchdog

All of this is optional.

```bash
./run.sh -nodes nodes.json -listen 127.0.0.1:9190      # GET /health and GET /metrics (Prometheus)
STATUS_TOKEN=... ./run.sh -nodes nodes.json -listen 0.0.0.0:9190   # off loopback a token is required, demanded on /metrics
./run.sh -nodes nodes.json -check-config                # validate the registry and exit (CONFIG=PASS / CONFIG=FAIL)
kill -HUP <pid>                                         # read the registry again; a registry that does not validate is refused
```

`/health` answers 200 while the process runs and names no node. `/metrics` carries
`hydra_node_healing_node_status{node,status}`, `..._last_check_timestamp_seconds`, `..._check_duration_seconds`,
`..._checks_total`, `..._transitions_total` and `hydra_node_healing_recovery_requests_total`. A gRPC node is polled
over one connection kept between polls; a connection that fails is dropped and dialed again.

