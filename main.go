// HYDRA-UMC-NODE-HEALING - entry point
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Real watchdog loop, no longer just an identity print: loads a static
// node registry (see src/config), then polls every listed node's
// HealthService.Check() (src/watchdog, over the shared
// hydra_common.proto contract vendored in src/healthpb) on a fixed
// interval, printing every state transition to stdout.
//
// A node entry with a "healthUrl" is probed over HTTP (src/watchdog/httpprobe.go)
// instead of gRPC, because the services in this ecosystem speak HTTP and none
// of them implements the gRPC HealthService.
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
	"strings"
	"os/signal"
	"syscall"
	"time"

	"github.com/JuanenRac/hydra-umc-node-healing/src/config"
	"github.com/JuanenRac/hydra-umc-node-healing/src/watchdog"
)

func main() {
	registryPath := flag.String("nodes", "nodes.example.json", "path to the JSON node registry to watch")
	orchestratorURL := flag.String("orchestrator-url", "", "HYDRA-UMC-ORCHESTRATOR base URL (e.g. http://127.0.0.1:8114) to request real recovery from; empty logs transitions only, same as before this flag existed")
	// The default is deliberately empty: a flag's default is printed by -h and by a
	// usage error (into the journal), and the token must never be. It is read from
	// the ORCHESTRATOR_TOKEN environment variable after parsing instead.
	orchestratorToken := flag.String("orchestrator-token", "", "bearer token sent with every recovery request (prefer the ORCHESTRATOR_TOKEN environment variable: a flag value is visible in the process list)")
	policy := watchdog.DefaultRecoveryPolicy()
	confirmAfter := flag.Duration("recover-confirm-after", policy.ConfirmAfter, "how long a node must stay down (or unhealthy) before recovery is requested; 0 requests it at once")
	startupGrace := flag.Duration("startup-grace", policy.StartupGrace, "no recovery is requested sooner than this after the watchdog starts")
	cooldown := flag.Duration("recover-cooldown", policy.Cooldown, "minimum time between two recovery requests for the same node")
	budget := flag.Int("recover-budget", policy.BudgetMax, "most recovery requests per 10 minutes across all nodes; past it the watchdog only logs (0 = unlimited)")
	journalPath := flag.String("journal", "", "append-only JSONL journal of every transition and recovery request, chained by SHA-256 and rotated by size; empty keeps no journal")
	journalMaxBytes := flag.Int64("journal-max-bytes", watchdog.DefaultJournalMaxBytes, "size at which the journal is rotated")
	journalBackups := flag.Int("journal-backups", watchdog.DefaultJournalBackups, "how many rotated journal files are kept")
	journalVerify := flag.Bool("journal-verify", false, "check the hash chain of the file given with -journal and exit")
	listen := flag.String("listen", "", "address (host:port) for the read-only status endpoint: GET /health (liveness) and GET /metrics (Prometheus); empty serves nothing. A non-loopback address needs -status-token")
	statusToken := flag.String("status-token", "", "bearer token demanded on /metrics (prefer the STATUS_TOKEN environment variable: a flag value is visible in the process list)")
	checkConfig := flag.Bool("check-config", false, "load and validate the node registry given with -nodes, print a summary and exit (0 = valid)")
	nodesFromPorts := flag.String("nodes-from-ports", "", "print a node registry built from the service registry of the ecosystem (contracts/ports.yaml of the SDK): every service with a health path, probed over HTTP; the services left out are listed on stderr; then exit")
	flag.Parse()
	if *nodesFromPorts != "" {
		text, readErr := os.ReadFile(*nodesFromPorts)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] fatal: %v\n", readErr)
			os.Exit(1)
		}
		registry, skipped, buildErr := config.NodesFromPorts(config.ParsePorts(string(text)))
		if buildErr != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] fatal: %v\n", buildErr)
			os.Exit(1)
		}
		fmt.Println(string(registry))
		if len(skipped) > 0 {
			fmt.Fprintf(os.Stderr, "[node-healing] left out (no port or no health path in the registry): %s\n", strings.Join(skipped, ", "))
		}
		return
	}
	var journal *watchdog.Journal
	if *journalPath != "" {
		var journalErr error
		journal, journalErr = watchdog.NewJournal(*journalPath, *journalMaxBytes, *journalBackups)
		if journalErr != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] fatal: %v\n", journalErr)
			os.Exit(1)
		}
	}
	if *journalVerify {
		if journal == nil {
			fmt.Fprintln(os.Stderr, "[node-healing] -journal-verify needs -journal PATH")
			os.Exit(1)
		}
		lines, verifyErr := journal.VerifyJournal()
		if verifyErr != nil {
			fmt.Printf("JOURNAL_CHAIN=FAIL %v\n", verifyErr)
			os.Exit(1)
		}
		fmt.Printf("JOURNAL_CHAIN=PASS %d line(s), chain intact\n", lines)
		return
	}
	if *checkConfig {
		nodes, loadErr := config.LoadNodes(*registryPath)
		if loadErr != nil {
			fmt.Printf("CONFIG=FAIL %v\n", loadErr)
			os.Exit(1)
		}
		httpNodes, grpcNodes, dispatch := 0, 0, 0
		for _, n := range nodes {
			if n.HealthURL != "" {
				httpNodes++
			} else {
				grpcNodes++
			}
			if n.DispatchNode != "" {
				dispatch++
			}
		}
		fmt.Printf("CONFIG=PASS %d node(s): %d over HTTP, %d over gRPC, %d with a dispatchNode\n", len(nodes), httpNodes, grpcNodes, dispatch)
		return
	}
	if *orchestratorToken == "" {
		*orchestratorToken = os.Getenv("ORCHESTRATOR_TOKEN")
	}
	if *statusToken == "" {
		*statusToken = os.Getenv("STATUS_TOKEN")
	}
	if *listen != "" {
		if listenErr := watchdog.ValidateListen(*listen, *statusToken); listenErr != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] fatal: %v\n", listenErr)
			os.Exit(1)
		}
	}
	policy.ConfirmAfter = *confirmAfter
	policy.StartupGrace = *startupGrace
	policy.Cooldown = *cooldown
	policy.BudgetMax = *budget

	fmt.Printf("HYDRA-UMC-NODE-HEALING v%s\n", Version)
	fmt.Println("High-availability watchdog: monitors every HydraNode heartbeat and triggers automatic failover to keep the swarm at zero downtime.")

	nodes, err := config.LoadNodes(*registryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[node-healing] fatal: %v\n", err)
		fmt.Fprintln(os.Stderr, "[node-healing] see nodes.example.json for the expected format; an entry with an \"address\" needs a running gRPC HealthService (hydra.common.v1), an entry with a \"healthUrl\" is probed with an HTTP GET (2xx = healthy).")
		os.Exit(1)
	}
	fmt.Printf("[node-healing] watching %d node(s) from %s\n", len(nodes), *registryPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var printf func(format string, args ...any) // nil prints to the console only
	if journal != nil {
		printf = journal.Printf(nil)
		if err := journal.Record("start", map[string]any{"version": Version, "nodes": len(nodes), "registry": *registryPath}); err != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] journal: cannot write %s: %v\n", *journalPath, err)
		}
		fmt.Printf("[node-healing] journal: %s\n", *journalPath)
	}
	var reactor watchdog.Reactor = watchdog.LogReactor{Printf: printf}
	if *orchestratorURL != "" {
		reactor = &watchdog.OrchestratorReactor{BaseURL: *orchestratorURL, Token: *orchestratorToken, Policy: policy, Context: ctx, Printf: printf}
		fmt.Printf("[node-healing] recovery requests will be sent to orchestrator at %s only for registry entries with a \"dispatchNode\", after %s down, %s after start-up, at most %d per 10 minutes and one per node every %s\n",
			*orchestratorURL, policy.ConfirmAfter, policy.StartupGrace, policy.BudgetMax, policy.Cooldown)
	}

	wd := watchdog.NewWatchdog(nodes, reactor)

	if *listen != "" {
		status := &watchdog.StatusServer{Watchdog: wd, Version: Version, Started: time.Now(), Token: *statusToken}
		if counter, ok := reactor.(watchdog.RecoveryCounter); ok {
			status.Recoveries = counter
		}
		go func() {
			if serveErr := status.Serve(ctx, *listen); serveErr != nil {
				fmt.Fprintf(os.Stderr, "[node-healing] status endpoint on %s stopped: %v\n", *listen, serveErr)
			}
		}()
		fmt.Printf("[node-healing] status endpoint: http://%s/health and /metrics\n", *listen)
	}

	// SIGHUP reads the registry again. A registry that does not load or does not validate is refused
	// and the nodes being watched stay as they were; a node that stays keeps its state.
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-reload:
				fresh, reloadErr := config.LoadNodes(*registryPath)
				if reloadErr != nil {
					fmt.Fprintf(os.Stderr, "[node-healing] reload refused, still watching the previous list: %v\n", reloadErr)
					if journal != nil {
						_ = journal.Record("reload-refused", map[string]any{"registry": *registryPath})
					}
					continue
				}
				wd.SetNodes(fresh)
				fmt.Printf("[node-healing] reloaded %s: watching %d node(s)\n", *registryPath, len(fresh))
				if journal != nil {
					_ = journal.Record("reload", map[string]any{"registry": *registryPath, "nodes": len(fresh)})
				}
			}
		}
	}()

	wd.Run(ctx)

	if journal != nil {
		_ = journal.Record("stop", nil)
	}
	fmt.Println("[node-healing] shutting down")
}
