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
	"sync"
	"os/signal"
	"syscall"
	"time"

	"github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/config"
	"github.com/JuanenRac/HYDRA-UMC-NODE-HEALING/src/watchdog"
)

func main() {
	registryPath := flag.String("nodes", "nodes.example.json", "path to the JSON node registry to watch")
	orchestratorURL := flag.String("orchestrator-url", "", "HYDRA-UMC-ORCHESTRATOR base URL (e.g. http://127.0.0.1:8114) to request real recovery from; empty logs transitions only, same as before this flag existed")
	// The default is deliberately empty: a flag's default is printed by -h and by a
	// usage error (into the journal), and the token must never be. It is read from
	// the ORCHESTRATOR_TOKEN environment variable after parsing instead.
	orchestratorToken := flag.String("orchestrator-token", "", "bearer token sent with every recovery request (prefer the ORCHESTRATOR_TOKEN environment variable: a flag value is visible in the process list)")
	healerCell := flag.String("healer-cell", "", "name of the cell this watchdog looks after; with --orchestrator-url it takes the lease healer-of-<cell> at the orchestrator and requests a recovery only while it holds it (empty: no lease, as before)")
	healerID := flag.String("healer-id", "", "who this watchdog is for the lease (letters, digits and . _ : -); default is the host name")
	healerLeaseTTL := flag.Duration("healer-lease-ttl", watchdog.DefaultLeaseTTL, "how long a grant of the lease lasts without a renewal (it is renewed every third of it)")
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
	eventsConfigPath := flag.String("events-config", "", "JSON file that turns on the events over MQTT (every transition of a node, and a proposal of a maintenance incident for a node that stays down); empty publishes nothing, as before this flag existed")
	swarmConfigPath := flag.String("swarm-config", "", "JSON file that adds to the registry the nodes announced in the state of SWARM-SYNC, with signed entries (see docs/INTEGRATION_CONTRACT.md); empty uses only the registry file, as before this flag existed")
	fencingConfigPath := flag.String("fencing-config", "", "JSON file that turns on fencing: before a recovery is requested the cell must confirm that the robots of the node are stopped or isolated, and nothing is requeued otherwise; needs -orchestrator-url; empty requests recoveries as before this flag existed")
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
		if *eventsConfigPath != "" {
			events, eventsErr := watchdog.LoadEventsConfig(*eventsConfigPath)
			if eventsErr != nil {
				fmt.Printf("EVENTS_CONFIG=FAIL %v\n", eventsErr)
				os.Exit(1)
			}
			fmt.Printf("EVENTS_CONFIG=PASS mqtt %s, incident proposals %s\n", onOff(events.MQTT.Enabled), onOff(events.IncidentProposals.Enabled))
		}
		if *swarmConfigPath != "" {
			swarmConfig, swarmErr := config.LoadSwarmConfig(*swarmConfigPath)
			if swarmErr != nil {
				fmt.Printf("SWARM_CONFIG=FAIL %v\n", swarmErr)
				os.Exit(1)
			}
			fmt.Printf("SWARM_CONFIG=PASS dynamic registry %s\n", onOff(swarmConfig.Enabled))
		}
		if *fencingConfigPath != "" {
			fencingConfig, fencingErr := watchdog.LoadFencingConfig(*fencingConfigPath)
			if fencingErr != nil {
				fmt.Printf("FENCING_CONFIG=FAIL %v\n", fencingErr)
				os.Exit(1)
			}
			fmt.Printf("FENCING_CONFIG=PASS fencing %s\n", onOff(fencingConfig.Enabled))
		}
		return
	}
	if *orchestratorToken == "" {
		*orchestratorToken = os.Getenv("ORCHESTRATOR_TOKEN")
	}
	if *statusToken == "" {
		*statusToken = os.Getenv("STATUS_TOKEN")
	}
	if *healerCell != "" {
		if *orchestratorURL == "" {
			fmt.Fprintln(os.Stderr, "[node-healing] fatal: --healer-cell needs --orchestrator-url (the lease lives at the orchestrator)")
			os.Exit(1)
		}
		if !watchdog.ValidLabel(watchdog.LeasePrefix + *healerCell) {
			fmt.Fprintln(os.Stderr, "[node-healing] fatal: --healer-cell must be letters, digits and . _ : - (the lease name is at most 128 characters)")
			os.Exit(1)
		}
		if *healerID != "" && !watchdog.ValidLabel(*healerID) {
			fmt.Fprintln(os.Stderr, "[node-healing] fatal: --healer-id must be letters, digits and . _ : -")
			os.Exit(1)
		}
		if *healerLeaseTTL < time.Second {
			fmt.Fprintln(os.Stderr, "[node-healing] fatal: --healer-lease-ttl must be at least one second")
			os.Exit(1)
		}
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

	var fencer watchdog.Fencer
	if *fencingConfigPath != "" {
		fencingConfig, fencingErr := watchdog.LoadFencingConfig(*fencingConfigPath)
		if fencingErr != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] fatal: --fencing-config: %v\n", fencingErr)
			os.Exit(1)
		}
		if fencingConfig.Enabled {
			if *orchestratorURL == "" {
				fmt.Fprintln(os.Stderr, "[node-healing] fatal: --fencing-config guards the recovery request, which needs --orchestrator-url")
				os.Exit(1)
			}
			httpFencer, fencerErr := watchdog.NewHTTPFencer(fencingConfig)
			if fencerErr != nil {
				fmt.Fprintf(os.Stderr, "[node-healing] fatal: --fencing-config: %v\n", fencerErr)
				os.Exit(1)
			}
			fencer = httpFencer
			fmt.Printf("[node-healing] fencing on: a recovery is requested only after the robots of the node are confirmed safe (%s)\n", fencingConfig.Mode)
		}
	}

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
		orchestratorReactor := &watchdog.OrchestratorReactor{BaseURL: *orchestratorURL, Token: *orchestratorToken, Policy: policy, Context: ctx, Printf: printf}
		if *healerCell != "" {
			holder := *healerID
			if holder == "" {
				host, _ := os.Hostname()
				holder = host
			}
			if !watchdog.ValidLabel(holder) {
				fmt.Fprintf(os.Stderr, "[node-healing] fatal: the host name %q cannot name a holder; give --healer-id\n", holder)
				os.Exit(1)
			}
			lease := &watchdog.Lease{BaseURL: *orchestratorURL, Token: *orchestratorToken, Cell: *healerCell, Holder: holder, TTL: *healerLeaseTTL, Printf: printf}
			orchestratorReactor.Lease = lease
			go lease.Run(ctx)
			fmt.Printf("[node-healing] lease %s as %s: a recovery is requested only while this watchdog holds it\n", lease.Name(), holder)
		}
		orchestratorReactor.Fencer = fencer
		reactor = orchestratorReactor
		fmt.Printf("[node-healing] recovery requests will be sent to orchestrator at %s only for registry entries with a \"dispatchNode\", after %s down, %s after start-up, at most %d per 10 minutes and one per node every %s\n",
			*orchestratorURL, policy.ConfirmAfter, policy.StartupGrace, policy.BudgetMax, policy.Cooldown)
	}

	var wdReactor watchdog.Reactor = reactor
	var eventReactor *watchdog.EventReactor
	if *eventsConfigPath != "" {
		events, eventsErr := watchdog.LoadEventsConfig(*eventsConfigPath)
		if eventsErr != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] fatal: --events-config: %v\n", eventsErr)
			os.Exit(1)
		}
		if events.MQTT.Enabled {
			target, targetErr := events.Target()
			if targetErr != nil {
				fmt.Fprintf(os.Stderr, "[node-healing] fatal: --events-config: %v\n", targetErr)
				os.Exit(1)
			}
			eventReactor = watchdog.NewEventReactor(events, target, printf)
			go eventReactor.Run(ctx)
			wdReactor = watchdog.MultiReactor{reactor, eventReactor}
			fmt.Printf("[node-healing] events: every transition goes to %s/<node>/state on %s; incident proposals %s\n", events.MQTT.TopicPrefix, events.MQTT.Broker, onOff(events.IncidentProposals.Enabled))
		}
	}

	// The nodes announced in the state of SWARM-SYNC are added to the static registry (static entries win). A read that fails
	// leaves the nodes as they are.
	var swarm *config.SwarmRegistry
	if *swarmConfigPath != "" {
		swarmConfig, swarmErr := config.LoadSwarmConfig(*swarmConfigPath)
		if swarmErr != nil {
			fmt.Fprintf(os.Stderr, "[node-healing] fatal: --swarm-config: %v\n", swarmErr)
			os.Exit(1)
		}
		if swarmConfig.Enabled {
			var registryErr error
			swarm, registryErr = config.NewSwarmRegistry(swarmConfig, printf)
			if registryErr != nil {
				fmt.Fprintf(os.Stderr, "[node-healing] fatal: --swarm-config: %v\n", registryErr)
				os.Exit(1)
			}
			if fetchErr := swarm.Fetch(ctx); fetchErr != nil {
				fmt.Fprintf(os.Stderr, "[node-healing] swarm registry: %v; watching the registry file until it can be read\n", fetchErr)
			}
			fmt.Printf("[node-healing] swarm registry on: nodes announced at %s are added to the registry file\n", swarmConfig.URL)
		}
	}
	var staticMu sync.Mutex
	staticNodes := nodes
	currentNodes := func() []watchdog.Node {
		staticMu.Lock()
		defer staticMu.Unlock()
		if swarm == nil {
			return staticNodes
		}
		return swarm.Merge(staticNodes)
	}

	wd := watchdog.NewWatchdog(currentNodes(), wdReactor)
	if swarm != nil {
		go swarm.Run(ctx, func() { wd.SetNodes(currentNodes()) })
	}

	if *listen != "" {
		status := &watchdog.StatusServer{Watchdog: wd, Version: Version, Started: time.Now(), Token: *statusToken}
		if counter, ok := reactor.(watchdog.RecoveryCounter); ok {
			status.Recoveries = counter
		}
		if eventReactor != nil {
			status.Events = eventReactor
		}
		if counter, ok := reactor.(watchdog.FenceCounter); ok && fencer != nil {
			status.Fences = counter
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
				staticMu.Lock()
				staticNodes = fresh
				staticMu.Unlock()
				all := currentNodes()
				wd.SetNodes(all)
				fmt.Printf("[node-healing] reloaded %s: watching %d node(s)\n", *registryPath, len(all))
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

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
