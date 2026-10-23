<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Build and run guide
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Build and Run

Run `build-test.bat` or `build-test.sh` first. They execute the deterministic
Go validation route without changing version or CHANGELOG. `build.bat` and
`build.sh` are release flows and increment the version before building (the shared build flow of the family).

`run.bat` and `run.sh` are local operator entry points. Use an explicit local
node inventory and inspect the resulting decision log; do not infer a real
restart or repair from a simulated recommendation.

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

