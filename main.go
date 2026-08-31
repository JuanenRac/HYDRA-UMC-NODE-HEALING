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
// Real failover is now wired in: --orchestrator-url points this at
// HYDRA-UMC-ORCHESTRATOR's own real POST /nodes/:node/recover
// (watchdog.OrchestratorReactor, new - the seam watchdog.Reactor always
// left open for exactly this). Omitted, this falls back to
// watchdog.LogReactor (detection-only, the previous default) - a real
// deployment with no Orchestrator running yet loses nothing by leaving
// this flag unset.
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
	orchestratorURL := flag.String("orchestrator-url", "", "HYDRA-UMC-ORCHESTRATOR base URL (e.g. http://127.0.0.1:8114) to request real recovery from; empty logs transitions only, same as before this flag existed")
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

	var reactor watchdog.Reactor = watchdog.LogReactor{}
	if *orchestratorURL != "" {
		reactor = watchdog.OrchestratorReactor{BaseURL: *orchestratorURL}
		fmt.Printf("[node-healing] real recovery requests will be sent to orchestrator at %s\n", *orchestratorURL)
	}

	wd := watchdog.NewWatchdog(nodes, reactor)
	wd.Run(ctx)

	fmt.Println("[node-healing] shutting down")
}
