<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Build and run guide
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Build and Run

Run `build-test.bat` or `build-test.sh` first. They execute the deterministic
Go validation route without changing version or CHANGELOG. `build.bat` and
`build.sh` are release flows and may increment metadata after successful tests.

`run.bat` and `run.sh` are local operator entry points. Use an explicit local
node inventory and inspect the resulting decision log; do not infer a real
restart or repair from a simulated recommendation.
