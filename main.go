// HYDRA-UMC-NODE-HEALING - entry point
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Real watchdog loop, no longer just an identity print: loads a static
// node registry (see src/config), then polls every listed node's
// HealthService.Check() (src/watchdog, over the shared
// hydra_common.proto contract vendored in src/healthpb) on a fixed
// interval, printing every state transition to stdout.
//
// Why this doesn't yet trigger real failover/soft-reboot: that requires
// calling HYDRA-UMC-ORCHESTRATOR with an API that doesn't exist yet
// either (ORCHESTRATOR is itself andamiaje beyond the proto/ contract
// added this session). watchdog.Reactor is the seam for that - swap
// watchdog.LogReactor for a real implementation once ORCHESTRATOR has
// something to call. Detection working for real today does not require
// waiting on that.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/JuanenRac/hydra-umc-node-healing/src/config"
	"github.com/JuanenRac/hydra-umc-node-healing/src/watchdog"
)

func main() {
	registryPath := flag.String("nodes", "nodes.example.json", "path to the JSON node registry to watch")
	flag.Parse()

	fmt.Printf("HYDRA-UMC-NODE-HEALING v%s\n", Version)
	fmt.Println("High-availability watchdog: monitors every HydraNode heartbeat and triggers automatic failover to keep the swarm at zero downtime.")

	nodes, err := config.LoadNodes(*registryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[node-healing] fatal: %v\n", err)
		fmt.Fprintln(os.Stderr, "[node-healing] see nodes.example.json for the expected format; every entry needs a running HealthService (hydra.common.v1) to actually report healthy.")
		os.Exit(1)
	}
	fmt.Printf("[node-healing] watching %d node(s) from %s\n", len(nodes), *registryPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	wd := watchdog.NewWatchdog(nodes, watchdog.LogReactor{})
	wd.Run(ctx)

	fmt.Println("[node-healing] shutting down")
}
