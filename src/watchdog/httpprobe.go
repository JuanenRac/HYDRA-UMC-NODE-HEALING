// HYDRA-UMC-NODE-HEALING - HTTP health probe
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// The gRPC HealthService this watchdog was written against is not
// implemented by any service in the ecosystem; they all expose a plain
// HTTP health route instead. A node with a "healthUrl" in the registry is
// probed with one GET and classified here, using the same Status values and
// the same retry policy as a gRPC node.
//
//	2xx          HEALTHY, unless a JSON body says otherwise ("status":
//	             "degraded" -> DEGRADED, "unhealthy"/"down"/"error" -> UNHEALTHY)
//	503, 5xx     UNHEALTHY: the node is alive enough to answer and says it is not well
//	timeout, refused, reset   UNREACHABLE (retried)
//	3xx, 4xx     INVALID: a wrong URL or a missing credential is a
//	             configuration problem, and retrying does not fix it
//
// A JSON body may also carry a "name"; when it does and it differs from the
// registry name the node is INVALID, exactly like a gRPC node that
// misreports its identity.
package watchdog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/JuanenRac/hydra-umc-node-healing/src/redact"
)

// maxProbeBodyBytes bounds what a node can make the watchdog read; a health
// answer is a few dozen bytes.
const maxProbeBodyBytes = 64 * 1024

// redactURL hides a password carried in the URL's userinfo, so a credential
// never reaches the journal through a log line.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable URL)"
	}
	return u.Redacted()
}

type httpHealthBody struct {
	// PersistenceHealthy is reported by services (the job queue) whose
	// "status" stays "ok" while they can no longer save their state.
	PersistenceHealthy *bool  `json:"persistenceHealthy"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail"`
	Message string `json:"message"`
}

func (w *Watchdog) httpClient() *http.Client {
	if w.HTTPClient != nil {
		return w.HTTPClient
	}
	return &http.Client{
		Timeout: w.CheckTimeout,
		// a redirect is never followed: a health URL that redirects is
		// pointing at something other than the node
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// attemptHTTPCheck probes the node and returns what it found; the text goes through the shared secret
// redaction, because it can quote what the node answered or the error of the connection.
func (w *Watchdog) attemptHTTPCheck(ctx context.Context, n Node) (status Status, detail string, retryable bool) {
	status, detail, retryable = w.probeHTTP(ctx, n)
	return status, redact.Secrets(detail), retryable
}

func (w *Watchdog) probeHTTP(ctx context.Context, n Node) (status Status, detail string, retryable bool) {
	reqCtx, cancel := context.WithTimeout(ctx, w.CheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, n.HealthURL, nil)
	if err != nil {
		return StatusInvalid, fmt.Sprintf("health URL %q is not usable: %v", redactURL(n.HealthURL), err), false
	}
	req.Header.Set("Accept", "application/json")
	for name, value := range n.Headers {
		req.Header.Set(name, value)
	}
	resp, err := w.httpClient().Do(req)
	if err != nil {
		return StatusUnreachable, err.Error(), true
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))

	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 500:
		return StatusMisconfigured, fmt.Sprintf("health URL %s answered %s - that is a configuration problem, not a node failure", redactURL(n.HealthURL), resp.Status), false
	case resp.StatusCode >= 500:
		return StatusUnhealthy, fmt.Sprintf("health URL answered %s", resp.Status), false
	}

	var body httpHealthBody
	if json.Unmarshal(raw, &body) == nil && body.Name != "" && body.Name != n.Name {
		return StatusInvalid, fmt.Sprintf("node at %s self-identified as %q, expected %q - refusing to trust its health state", redactURL(n.HealthURL), body.Name, n.Name), false
	}
	text := body.Detail
	if text == "" {
		text = body.Message
	}
	switch strings.ToLower(body.Status) {
	case "degraded", "warn", "warning":
		return StatusDegraded, text, false
	case "unhealthy", "down", "error", "fail", "failed":
		return StatusUnhealthy, text, false
	}
	if body.PersistenceHealthy != nil && !*body.PersistenceHealthy {
		if text == "" {
			text = "the service reports that it cannot persist its state"
		}
		return StatusDegraded, text, false
	}
	return StatusHealthy, text, false
}
