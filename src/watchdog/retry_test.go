// HYDRA-UMC-NODE-HEALING - watchdog package: retry policy tests
// Copyright (C) 2026 JuanenRac (Electro Hobby 3D) <electrohobby3d@gmail.com>
// GPL-3.0 - see LICENSE
package watchdog

import (
	"testing"
	"time"
)

func TestRetryPolicy_Validate_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		policy RetryPolicy
	}{
		{"zero max attempts", RetryPolicy{MaxAttempts: 0, BaseDelay: time.Second, MaxDelay: time.Second}},
		{"negative max attempts", RetryPolicy{MaxAttempts: -1, BaseDelay: time.Second, MaxDelay: time.Second}},
		{"zero base delay", RetryPolicy{MaxAttempts: 3, BaseDelay: 0, MaxDelay: time.Second}},
		{"negative base delay", RetryPolicy{MaxAttempts: 3, BaseDelay: -time.Second, MaxDelay: time.Second}},
		{"max delay below base delay", RetryPolicy{MaxAttempts: 3, BaseDelay: 2 * time.Second, MaxDelay: time.Second}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.policy.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want an error for %+v", c.policy)
			}
		})
	}
}

func TestRetryPolicy_Validate_AcceptsSanePolicy(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a sane policy", err)
	}
}

func TestRetryPolicy_Validate_AcceptsMaxDelayEqualToBaseDelay(t *testing.T) {
	// Boundary: MaxDelay == BaseDelay is a valid (if degenerate, no real
	// backoff growth) policy - only MaxDelay < BaseDelay is rejected.
	policy := RetryPolicy{MaxAttempts: 2, BaseDelay: 100 * time.Millisecond, MaxDelay: 100 * time.Millisecond}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil when MaxDelay == BaseDelay", err)
	}
}

func TestRetryPolicy_Backoff_ZeroOrNegativeAttemptReturnsZero(t *testing.T) {
	policy := DefaultRetryPolicy()
	if got := policy.Backoff(0); got != 0 {
		t.Errorf("Backoff(0) = %v, want 0", got)
	}
	if got := policy.Backoff(-1); got != 0 {
		t.Errorf("Backoff(-1) = %v, want 0", got)
	}
}

func TestRetryPolicy_Backoff_ExponentialGrowth(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	want := map[int]time.Duration{
		1: 100 * time.Millisecond,
		2: 200 * time.Millisecond,
		3: 400 * time.Millisecond,
		4: 800 * time.Millisecond,
	}
	for attempt, wantDelay := range want {
		if got := policy.Backoff(attempt); got != wantDelay {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, wantDelay)
		}
	}
}

func TestRetryPolicy_Backoff_CappedAtMaxDelay(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	// Attempt 4 would be 800ms uncapped; attempt 5 would be 1600ms
	// uncapped - both attempt 5 and every attempt after it must be
	// clamped to exactly MaxDelay, never exceed it.
	for _, attempt := range []int{5, 6, 20} {
		if got := policy.Backoff(attempt); got != policy.MaxDelay {
			t.Errorf("Backoff(%d) = %v, want MaxDelay %v", attempt, got, policy.MaxDelay)
		}
	}
}

func TestRetryPolicy_Backoff_NeverExceedsMaxDelayEvenAtBoundary(t *testing.T) {
	// Boundary: BaseDelay itself already equals MaxDelay - Backoff must
	// still return exactly MaxDelay, not one doubling past it.
	policy := RetryPolicy{MaxAttempts: 5, BaseDelay: 500 * time.Millisecond, MaxDelay: 500 * time.Millisecond}
	for attempt := 1; attempt <= 4; attempt++ {
		if got := policy.Backoff(attempt); got != 500*time.Millisecond {
			t.Errorf("Backoff(%d) = %v, want 500ms", attempt, got)
		}
	}
}

func TestDefaultRetryPolicy_IsValid(t *testing.T) {
	if err := DefaultRetryPolicy().Validate(); err != nil {
		t.Fatalf("DefaultRetryPolicy().Validate() = %v, want nil", err)
	}
}
