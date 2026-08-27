// HYDRA-UMC-NODE-HEALING - watchdog package: retry policy
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
//
// A bounded retry policy for checkNode's dial/RPC attempts against a
// single node within one poll tick. Real network blips (a brief
// connection refusal while a service is restarting) should not
// immediately flip a node's classification to UNREACHABLE and fire a
// Reactor transition - but an open-ended retry loop would risk one slow
// node stalling pollAll's WaitGroup past the next tick. Bounded attempts
// with a deterministic, verifiable backoff is the middle ground.
package watchdog

import (
	"fmt"
	"time"
)

// RetryPolicy bounds how many times checkNode retries a single node's
// dial/RPC after a transport-level failure (connection refused, dial
// timeout, RPC error) before giving up for this poll tick and
// classifying it StatusUnreachable. Deliberately NOT retried under this
// policy: a node that answers but misreports its own identity (see
// StatusInvalid in watchdog.go) - that is not a transient network blip a
// retry could ever fix, so retrying it would only delay an honest
// rejection.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy: 3 attempts, 250ms base delay, capped at 2s.
// Bounded so that the worst case (250ms + 500ms = 750ms of backoff
// across 3 attempts) stays well under Watchdog's own default 5s poll
// Interval - a fully-exhausted retry loop for one node must never be
// able to push a poll tick into overlapping the next one.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 250 * time.Millisecond, MaxDelay: 2 * time.Second}
}

// Validate rejects a policy that could not produce a sane retry loop.
// Called by NewWatchdog on construction and safe to call again after
// overriding Watchdog.RetryPolicy in a test or a future config loader.
func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 {
		return fmt.Errorf("retry policy MaxAttempts must be >= 1, got %d", p.MaxAttempts)
	}
	if p.BaseDelay <= 0 {
		return fmt.Errorf("retry policy BaseDelay must be positive, got %s", p.BaseDelay)
	}
	if p.MaxDelay < p.BaseDelay {
		return fmt.Errorf("retry policy MaxDelay (%s) must be >= BaseDelay (%s)", p.MaxDelay, p.BaseDelay)
	}
	return nil
}

// Backoff returns the delay to wait before the attempt that follows
// attempt number `attempt` (1-indexed: Backoff(1) is the wait after the
// first attempt failed, before making the second attempt). Exponential
// doubling from BaseDelay, capped at MaxDelay, with no random jitter -
// deterministic on purpose so every value is directly assertable in
// tests rather than only bounded by a range.
func (p RetryPolicy) Backoff(attempt int) time.Duration {
	if attempt < 1 {
		return 0
	}
	delay := p.BaseDelay
	for i := 1; i < attempt; i++ {
		if delay >= p.MaxDelay {
			return p.MaxDelay
		}
		delay *= 2
	}
	if delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}
