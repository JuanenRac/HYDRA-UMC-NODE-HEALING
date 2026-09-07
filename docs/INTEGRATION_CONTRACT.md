<!-- =============================================================================
HYDRA-UMC-NODE-HEALING - Integration contract
Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
GPL-3.0-or-later - see LICENSE
============================================================================= -->

# Integration Contract

The integration boundary consumes a versioned health snapshot and produces a
recommendation with reason and retry/backoff context. Snapshots without a
stable node identity, timestamp or health state must be rejected by a future
adapter. Repeated observations must remain idempotent.

Any real restart, package change or credential action needs authenticated,
auditable authority outside this decision-only contract.
