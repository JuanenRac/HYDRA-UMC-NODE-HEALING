// HYDRA-UMC-NODE-HEALING - watchdog package: lease.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The lease of a healer. Two watchdogs on the same cell (two controller boards, or a controller and a
// development machine) would both ask the orchestrator to recover the same node. A watchdog takes the lease
// "healer-of-<cell>" at the orchestrator and keeps renewing it; only the holder shows the lease with its recovery
// requests, and the orchestrator refuses the recovery of anyone else. A watchdog that is not the holder keeps
// watching and logging, and asks for nothing; when the holder stops renewing, the lease expires and another
// watchdog takes it over with a higher token.
package watchdog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LeasePrefix is the start of the name of every healer lease at the orchestrator.
const LeasePrefix = "healer-of-"

// DefaultLeaseTTL is how long a grant lasts without a renewal; it is renewed every third of it.
const DefaultLeaseTTL = 15 * time.Second

// Lease keeps one lease at the orchestrator. Use &Lease{...}, not a bare value.
type Lease struct {
	// BaseURL is the HTTP API of the orchestrator.
	BaseURL string
	// Token is the bearer token of the orchestrator, when it has one.
	Token string
	// Cell names the cell this watchdog looks after; the lease is "healer-of-<Cell>".
	Cell string
	// Holder says who this watchdog is (letters, digits and . _ : -).
	Holder string
	// TTL is the lease time asked for (DefaultLeaseTTL when zero).
	TTL    time.Duration
	Client *http.Client
	Printf func(format string, args ...any)

	mu    sync.Mutex
	held  bool
	token uint64
}

// Name is the name of the lease at the orchestrator.
func (l *Lease) Name() string { return LeasePrefix + l.Cell }

func (l *Lease) ttl() time.Duration {
	if l.TTL > 0 {
		return l.TTL
	}
	return DefaultLeaseTTL
}

func (l *Lease) client() *http.Client {
	if l.Client != nil {
		return l.Client
	}
	return &http.Client{Timeout: defaultRecoveryTimeout}
}

func (l *Lease) printf(format string, args ...any) {
	if l.Printf != nil {
		l.Printf(format, args...)
		return
	}
	fmt.Printf(format, args...)
}

// Held says whether this watchdog holds the lease right now, as far as it knows.
func (l *Lease) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held
}

// Apply puts the proof of the lease on a recovery request. It reports false when this watchdog does not hold it.
func (l *Lease) Apply(req *http.Request) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return false
	}
	req.Header.Set("X-Healer-Lease", l.Name())
	req.Header.Set("X-Healer-Id", l.Holder)
	req.Header.Set("X-Healer-Token", strconv.FormatUint(l.token, 10))
	return true
}

func (l *Lease) post(ctx context.Context, action string, body map[string]any) (int, []byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	endpoint := fmt.Sprintf("%s/leases/%s/%s", strings.TrimSuffix(l.BaseURL, "/"), url.PathEscape(l.Name()), action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.Token != "" {
		req.Header.Set("Authorization", "Bearer "+l.Token)
	}
	resp, err := l.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, answer, nil
}

func (l *Lease) setHeld(held bool, token uint64) {
	l.mu.Lock()
	changed := l.held != held
	l.held, l.token = held, token
	l.mu.Unlock()
	if changed {
		if held {
			l.printf("[node-healing] this watchdog (%s) now holds the lease %s\n", l.Holder, l.Name())
		} else {
			l.printf("[node-healing] this watchdog (%s) no longer holds the lease %s: it keeps watching and asks for no recovery\n", l.Holder, l.Name())
		}
	}
}

// Step asks for the lease, or renews it when this watchdog holds it. A refusal or an unreachable orchestrator means "not the
// holder": a healer that cannot prove it holds the lease does not recover anything.
func (l *Lease) Step(ctx context.Context) {
	l.mu.Lock()
	held, token := l.held, l.token
	l.mu.Unlock()
	body := map[string]any{"holder": l.Holder, "ttlMs": l.ttl().Milliseconds()}
	action := "acquire"
	if held {
		action = "renew"
		body["token"] = token
	}
	status, answer, err := l.post(ctx, action, body)
	if err != nil {
		if ctx.Err() == nil {
			l.printf("[node-healing] lease %s: orchestrator not reached: %v\n", l.Name(), err)
		}
		// the lease may still be ours for a moment, but without a confirmation it is not safe to count on it for long: keep it
		// until the next step and drop it if that one fails as well
		if held {
			l.setHeld(false, 0)
		}
		return
	}
	if status != http.StatusOK {
		l.setHeld(false, 0)
		return
	}
	var granted struct {
		Token uint64 `json:"token"`
	}
	if json.Unmarshal(answer, &granted) != nil || granted.Token == 0 {
		l.setHeld(false, 0)
		return
	}
	l.setHeld(true, granted.Token)
}

// Run keeps the lease until the context ends, then gives it up so the next watchdog does not wait for it to expire.
func (l *Lease) Run(ctx context.Context) {
	interval := l.ttl() / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	l.Step(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			l.Release()
			return
		case <-ticker.C:
			l.Step(ctx)
		}
	}
}

// Release gives the lease up (best effort, short timeout).
func (l *Lease) Release() {
	l.mu.Lock()
	held, token := l.held, l.token
	l.held = false
	l.mu.Unlock()
	if !held {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _ = l.post(ctx, "release", map[string]any{"holder": l.Holder, "token": token})
}

// ValidLabel says whether text can name a lease or a holder: 1 to 128 letters, digits or . _ : -
func ValidLabel(text string) bool {
	if text == "" || len(text) > 128 {
		return false
	}
	for _, c := range text {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}
