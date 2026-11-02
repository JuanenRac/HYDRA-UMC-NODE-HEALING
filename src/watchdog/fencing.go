// HYDRA-UMC-NODE-HEALING - watchdog package: fencing.go
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0-or-later - see LICENSE
//
// Fencing: do not give the work of a node to another cell while the first one may still be moving a robot.
//
// A recovery asks the orchestrator to requeue the missions of a node. A mission that is in progress moves hardware, and a node
// the watchdog cannot reach is not necessarily a node that stopped: the network between them may be what broke. So, before a
// recovery request leaves, the Fencer must say the robot is confirmed safe. This is safety, so every doubt is a refusal: an
// answer that is not understood, a timeout, an error, a missing credential or a state that is not on the list of safe ones all mean
// "not safe", and the recovery is not requested (the missions stay where they are).
//
// Two ways to confirm, chosen in the configuration file (-fencing-config):
//
//	"confirm"  the cell is asked to hold the robot (POST holdUrl, optional) and its state is read (GET statusUrl) until it is one
//	           of safeStates, or confirmTimeoutSeconds pass. This needs the cell (HYDRA-UMC-SERVER / the firmware through the
//	           safety zones) to offer those two routes; they are named in the file, not guessed here.
//	"lease"    the robot stops by itself when the heartbeat of its lease is not renewed for leaseTtlSeconds. The node must have been
//	           failing for longer than that, plus marginSeconds, before the missions are given away.
//
// Fencing is off unless the file is given with enabled true. Without it the recovery works as it always did.
package watchdog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// FenceResult is the verdict of a Fencer.
type FenceResult struct {
	Safe   bool
	Reason string
}

// Fencer confirms that the robots of a node are safe before its missions are requeued.
type Fencer interface {
	// Fence is asked with the name the node has in the dispatch and for how long the watchdog has seen it failing.
	Fence(ctx context.Context, node Node, target string, failingFor time.Duration) FenceResult
}

// FencingConfig is the file given with -fencing-config.
type FencingConfig struct {
	Enabled bool `json:"enabled"`
	// Mode is "confirm" (default) or "lease".
	Mode string `json:"mode"`
	// HoldURL is POSTed to ask the cell to hold the robot; {node} is replaced by the dispatch node. Optional.
	HoldURL string `json:"holdUrl"`
	// StatusURL is read to know the state of the robot; {node} is replaced as above. Needed in "confirm" mode.
	StatusURL string `json:"statusUrl"`
	// TokenFile holds a bearer token for both routes.
	TokenFile string `json:"tokenFile"`
	// SafeStates are the states in {"state": "..."} that count as safe (lower case). Default stopped, held, isolated.
	SafeStates []string `json:"safeStates"`
	// ConfirmTimeoutSeconds is how long to wait for a safe state after the hold (1 to 300, 10 by default).
	ConfirmTimeoutSeconds int `json:"confirmTimeoutSeconds"`
	// PollMillis is the pause between two reads of the state (100 to 10000, 500 by default).
	PollMillis int `json:"pollMillis"`
	// LeaseTtlSeconds, in "lease" mode, is the time after which the robot stops by itself.
	LeaseTtlSeconds int `json:"leaseTtlSeconds"`
	// MarginSeconds is added to the lease time (0 to 600, 5 by default when the file does not say).
	MarginSeconds *int `json:"marginSeconds"`
	// AllowPlaintext lets a cell that is not on this machine be reached over http.
	AllowPlaintext bool `json:"allowPlaintext"`
}

var defaultSafeStates = []string{"stopped", "held", "isolated"}

func loopbackURLHost(host string) bool {
	host = strings.Trim(host, "[]")
	return strings.EqualFold(host, "localhost") || host == "::1" || strings.HasPrefix(host, "127.")
}

func checkTemplate(name, template string, allowPlaintext bool) error {
	if template == "" {
		return nil
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(template, "http://"), "https://")
	if authority, _, _ := strings.Cut(rest, "/"); strings.Contains(authority, "{") {
		return fmt.Errorf("fencing config: %q must not put the node in the host", name)
	}
	probe := strings.ReplaceAll(template, "{node}", "x")
	u, err := url.Parse(probe)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("fencing config: %q must be an http:// or https:// URL", name)
	}
	if strings.Contains(u.Host, "{") || u.User != nil {
		return fmt.Errorf("fencing config: %q must not put the node or a password in the host", name)
	}
	if u.Scheme == "http" && !loopbackURLHost(u.Hostname()) && !allowPlaintext {
		return fmt.Errorf("fencing config: a cell that is not on this machine needs https for %q, or \"allowPlaintext\" set on purpose", name)
	}
	return nil
}

// Validate refuses a configuration that cannot confirm anything or is unsafe.
func (c FencingConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.mode() {
	case "confirm":
		if c.StatusURL == "" {
			return fmt.Errorf("fencing config: mode \"confirm\" needs \"statusUrl\" (without reading the state nothing can be confirmed)")
		}
	case "lease":
		if c.LeaseTtlSeconds < 1 || c.LeaseTtlSeconds > 86400 {
			return fmt.Errorf("fencing config: mode \"lease\" needs \"leaseTtlSeconds\" from 1 to 86400")
		}
	default:
		return fmt.Errorf("fencing config: \"mode\" must be \"confirm\" or \"lease\"")
	}
	if err := checkTemplate("holdUrl", c.HoldURL, c.AllowPlaintext); err != nil {
		return err
	}
	if err := checkTemplate("statusUrl", c.StatusURL, c.AllowPlaintext); err != nil {
		return err
	}
	if c.ConfirmTimeoutSeconds < 0 || c.ConfirmTimeoutSeconds > 300 {
		return fmt.Errorf("fencing config: \"confirmTimeoutSeconds\" must be from 1 to 300")
	}
	if c.PollMillis < 0 || (c.PollMillis != 0 && (c.PollMillis < 100 || c.PollMillis > 10000)) {
		return fmt.Errorf("fencing config: \"pollMillis\" must be from 100 to 10000")
	}
	if c.MarginSeconds != nil && (*c.MarginSeconds < 0 || *c.MarginSeconds > 600) {
		return fmt.Errorf("fencing config: \"marginSeconds\" must be from 0 to 600")
	}
	for _, s := range c.SafeStates {
		if s == "" || s != strings.ToLower(s) || len(s) > 32 {
			return fmt.Errorf("fencing config: \"safeStates\" holds lower case words of at most 32 characters")
		}
	}
	return nil
}

func (c FencingConfig) mode() string {
	if c.Mode == "" {
		return "confirm"
	}
	return c.Mode
}

// LoadFencingConfig reads and validates the file; an unknown key is an error.
func LoadFencingConfig(path string) (FencingConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return FencingConfig{}, fmt.Errorf("reading fencing config %q: %w", path, err)
	}
	var c FencingConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return FencingConfig{}, fmt.Errorf("parsing fencing config %q: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return FencingConfig{}, err
	}
	return c, nil
}

// HTTPFencer is the Fencer built from a FencingConfig.
type HTTPFencer struct {
	Config FencingConfig
	Client *http.Client
	token  string
	// Sleep defaults to a wait that ends with the context; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) bool
}

// NewHTTPFencer reads the token named by the configuration.
func NewHTTPFencer(c FencingConfig) (*HTTPFencer, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	f := &HTTPFencer{Config: c}
	if c.TokenFile != "" {
		raw, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("fencing config: cannot read the token file: %w", err)
		}
		f.token = strings.TrimSpace(string(raw))
		if len(f.token) < 16 {
			return nil, fmt.Errorf("fencing config: the token file holds fewer than 16 characters")
		}
	}
	return f, nil
}

func (f *HTTPFencer) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (f *HTTPFencer) wait(ctx context.Context, d time.Duration) bool {
	if f.Sleep != nil {
		return f.Sleep(ctx, d)
	}
	return sleepOrDone(ctx, d)
}

func expand(template, target string) string {
	return strings.ReplaceAll(template, "{node}", url.PathEscape(target))
}

func (f *HTTPFencer) request(ctx context.Context, method, target string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	return f.client().Do(req)
}

// Fence implements Fencer. Everything that is not a clear "safe" is "not safe".
func (f *HTTPFencer) Fence(ctx context.Context, node Node, target string, failingFor time.Duration) FenceResult {
	if f.Config.mode() == "lease" {
		margin := 5
		if f.Config.MarginSeconds != nil {
			margin = *f.Config.MarginSeconds
		}
		need := time.Duration(f.Config.LeaseTtlSeconds+margin) * time.Second
		if failingFor < need {
			return FenceResult{false, fmt.Sprintf("the lease of the robot may still be alive: the node has been failing for %s and %s are needed", failingFor.Round(time.Second), need)}
		}
		return FenceResult{true, fmt.Sprintf("the lease of the robot has expired (failing for %s, needed %s)", failingFor.Round(time.Second), need)}
	}

	if f.Config.HoldURL != "" {
		payload, _ := json.Marshal(map[string]string{"node": target, "reason": "node-healing recovery"})
		resp, err := f.request(ctx, http.MethodPost, expand(f.Config.HoldURL, target), payload)
		if err != nil {
			return FenceResult{false, "the cell could not be asked to hold the robot: " + redactError(err)}
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return FenceResult{false, fmt.Sprintf("the cell refused to hold the robot (%s)", resp.Status)}
		}
	}

	timeout := time.Duration(f.Config.ConfirmTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	poll := time.Duration(f.Config.PollMillis) * time.Millisecond
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	safe := f.Config.SafeStates
	if len(safe) == 0 {
		safe = defaultSafeStates
	}
	deadline := time.Now().Add(timeout)
	last := "no answer"
	for {
		state, err := f.readState(ctx, expand(f.Config.StatusURL, target))
		if err != nil {
			last = err.Error()
		} else {
			for _, s := range safe {
				if state == s {
					return FenceResult{true, fmt.Sprintf("the cell reports the robot %s", state)}
				}
			}
			last = fmt.Sprintf("the cell reports %q, which is not a safe state", state)
		}
		if time.Now().Add(poll).After(deadline) || ctx.Err() != nil {
			return FenceResult{false, fmt.Sprintf("no confirmation in %s: %s", timeout, last)}
		}
		if !f.wait(ctx, poll) {
			return FenceResult{false, "no confirmation: the watchdog is stopping"}
		}
	}
}

// redactError keeps the useful part of a transport error without the URL, which can carry the name of the cell.
func redactError(err error) string {
	if e, ok := err.(*url.Error); ok {
		return e.Err.Error()
	}
	return err.Error()
}

func (f *HTTPFencer) readState(ctx context.Context, target string) (string, error) {
	resp, err := f.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("the state of the robot could not be read: %s", redactError(err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the cell answered %s to the state request", resp.Status)
	}
	var body struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.State == "" {
		return "", fmt.Errorf("the state of the robot is not understood")
	}
	return strings.ToLower(body.State), nil
}
