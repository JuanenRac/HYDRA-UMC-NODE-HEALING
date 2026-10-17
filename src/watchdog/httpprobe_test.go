// HYDRA-UMC-NODE-HEALING - HTTP health probe tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// Real HTTP round-trips against httptest servers: each response a service
// can give is classified once, and a transport failure is retried while a
// misconfiguration is not.
package watchdog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func probeWatchdog() *Watchdog {
	w := NewWatchdog(nil, nil)
	w.CheckTimeout = time.Second
	w.RetryPolicy = RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
	return w
}

func TestHTTPProbe_ClassifiesEveryAnswer(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   Status
	}{
		{"plain 200", 200, ``, StatusHealthy},
		{"ok json", 200, `{"status":"ok","name":"node-a"}`, StatusHealthy},
		{"degraded json", 200, `{"status":"degraded","detail":"disk 90%"}`, StatusDegraded},
		{"unhealthy json", 200, `{"status":"down"}`, StatusUnhealthy},
		{"503", 503, ``, StatusUnhealthy},
		{"500", 500, ``, StatusUnhealthy},
		{"404 is a wrong URL", 404, ``, StatusInvalid},
		{"401 is a missing credential", 401, ``, StatusInvalid},
		{"wrong name", 200, `{"status":"ok","name":"somebody-else"}`, StatusInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
				rw.WriteHeader(c.status)
				_, _ = rw.Write([]byte(c.body))
			}))
			defer srv.Close()
			got, detail := probeWatchdog().CheckOnce(context.Background(), Node{Name: "node-a", HealthURL: srv.URL})
			if got != c.want {
				t.Fatalf("status = %s (%s), want %s", got, detail, c.want)
			}
		})
	}
}

func TestHTTPProbe_ARedirectIsNotFollowed(t *testing.T) {
	var hits int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		rw.WriteHeader(200)
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, elsewhere.URL, http.StatusFound)
	}))
	defer srv.Close()
	got, _ := probeWatchdog().CheckOnce(context.Background(), Node{Name: "node-a", HealthURL: srv.URL})
	if got != StatusInvalid || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("status = %s, redirect target hit %d times; want INVALID and 0", got, hits)
	}
}

func TestHTTPProbe_RefusedConnectionIsUnreachableAndRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens there any more
	got, _ := probeWatchdog().CheckOnce(context.Background(), Node{Name: "node-a", HealthURL: url})
	if got != StatusUnreachable {
		t.Fatalf("status = %s, want UNREACHABLE", got)
	}
}

func TestHTTPProbe_MisconfigurationIsNotRetried(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		rw.WriteHeader(404)
	}))
	defer srv.Close()
	probeWatchdog().CheckOnce(context.Background(), Node{Name: "node-a", HealthURL: srv.URL})
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("a 404 was requested %d times, want exactly 1", hits)
	}
}

func TestHTTPProbe_ASlowNodeIsUnreachableNotHung(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	w := probeWatchdog()
	w.CheckTimeout = 50 * time.Millisecond
	start := time.Now()
	got, _ := w.CheckOnce(context.Background(), Node{Name: "node-a", HealthURL: srv.URL})
	if got != StatusUnreachable || time.Since(start) > 2*time.Second {
		t.Fatalf("status = %s after %v, want UNREACHABLE well within the timeout budget", got, time.Since(start))
	}
}

func TestHTTPProbe_TransitionsFireOncePerChange(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		if healthy.Load() {
			rw.WriteHeader(200)
			return
		}
		rw.WriteHeader(503)
	}))
	defer srv.Close()
	rec := &recordingReactor{}
	w := NewWatchdog([]Node{{Name: "node-a", HealthURL: srv.URL}}, rec)
	w.CheckTimeout = time.Second
	w.pollOne(context.Background(), w.Nodes[0])
	w.pollOne(context.Background(), w.Nodes[0])
	healthy.Store(false)
	w.pollOne(context.Background(), w.Nodes[0])
	w.pollOne(context.Background(), w.Nodes[0])
	got := rec.snapshot()
	want := []string{"node-a:UNKNOWN->HEALTHY", "node-a:HEALTHY->UNHEALTHY"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}
}
