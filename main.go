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
	"os/signal"
	"syscall"

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
	flag.Parse()
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
	if *orchestratorToken == "" {
		*orchestratorToken = os.Getenv("ORCHESTRATOR_TOKEN")
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
	wd.Run(ctx)

	if journal != nil {
		_ = journal.Record("stop", nil)
	}
	fmt.Println("[node-healing] shutting down")
}
