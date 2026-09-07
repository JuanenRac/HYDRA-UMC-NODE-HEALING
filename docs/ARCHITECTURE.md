<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Architecture guide
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Architecture

This Go service models node health, evaluates recovery eligibility and records
the decision path. `nodes.example.json` is a local example input; production
node inventory must be supplied by an authenticated deployment adapter.

Recovery planning is not permission to restart, reflash or reconfigure a node.
Those side effects remain outside this repository until a separately reviewed
adapter and audit trail exist.
