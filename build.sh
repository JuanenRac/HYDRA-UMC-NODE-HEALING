#!/usr/bin/env bash
# HYDRA-UMC-NODE-HEALING - build.sh
# Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
# GPL-3.0 - see LICENSE
set -euo pipefail
cd "$(dirname "$0")"

# Keep the window open if this was double-clicked (e.g. from a file
# manager) instead of run from an already-open terminal - fires on
# success AND on a `set -e` early exit alike, but only prompts when
# stdin is actually a terminal (never in CI/piped/non-interactive runs).
trap '[ -t 0 ] && read -r -p "Press Enter to close..." _' EXIT

echo "=== HYDRA-UMC-NODE-HEALING build ==="
# Bump the real, native version FIRST, then sync the manifest to match
# (--sync) - never the other way around, or bump_manifest_version.py's
# own no-flag path bumps native+manifest together and this next line
# bumps native a second time, leaving it one step ahead of the manifest.
python3 bump_version.py || exit 1
python3 bump_manifest_version.py --sync || exit 1

mkdir -p build
go build -o build/hydra-umc-node-healing .

echo "Build OK: build/hydra-umc-node-healing"
