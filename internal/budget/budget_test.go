package budget

import (
	"context"
	"errors"
	"testing"
	"time"
)

// withFakeDeadline builds a context whose deadline is `remaining` in the
// future of the fake clock, and points the package clock at that fake
// "now" for the duration of the test. This makes budget math deterministic
// without any real sleeping.
func withFakeDeadline(t *testing.T, remaining time.Duration) context.Context {
	t.Helper()

	// Base the fake clock on the real wall clock (not an arbitrary fixed
	// date) so context's own internal timer — which always fires against
	// real time, regardless of what our package clock is set to — agrees
	// with the budget math instead of treating the deadline as already
	// expired.
	fakeNow := time.Now()
	deadline := fakeNow.Add(remaining)

	orig := now
	now = func() time.Time { return fakeNow }
	t.Cleanup(func() { now = orig })

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	return ctx
}

// Star test 1: child timeout can never exceed the parent's remaining
// budget, even when the configured max timeout is much larger.
func TestChildTimeout_NeverExceedsParentDeadline(t *testing.T) {
	ctx := withFakeDeadline(t, 300*time.Millisecond)

	got, err := ChildTimeout(ctx, 2*time.Second, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := 250 * time.Millisecond
	if got != want {
		t.Fatalf("ChildTimeout() = %v, want %v", got, want)
	}
}

// Star test 2: if the reserve already consumes all remaining budget, the
// call should never be attempted — fail fast with ErrBudgetExhausted.
func TestChildTimeout_BudgetExhausted(t *testing.T) {
	ctx := withFakeDeadline(t, 40*time.Millisecond)

	got, err := ChildTimeout(ctx, 2*time.Second, 50*time.Millisecond)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	if got != 0 {
		t.Fatalf("got = %v, want 0", got)
	}
}

// Star test 3: a generous remaining budget doesn't override the caller's
// configured max timeout for the child operation.
func TestChildTimeout_RespectsConfiguredMax(t *testing.T) {
	ctx := withFakeDeadline(t, 5*time.Second)

	got, err := ChildTimeout(ctx, 500*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := 500 * time.Millisecond
	if got != want {
		t.Fatalf("ChildTimeout() = %v, want %v", got, want)
	}
}

// Star test 4: budget propagates correctly across multiple simulated hops
// off a single total deadline.
func TestChildTimeout_MultiHopPropagation(t *testing.T) {
	ctx := withFakeDeadline(t, 1*time.Second)

	// Hop A uses 200ms of wall-clock time. Simulate that by advancing the
	// fake clock and recomputing budget from there.
	hopADuration := 200 * time.Millisecond
	advance(t, hopADuration)

	hopBTimeout, err := ChildTimeout(ctx, 2*time.Second, 0)
	if err != nil {
		t.Fatalf("hop B: unexpected error: %v", err)
	}
	if want := 800 * time.Millisecond; hopBTimeout != want {
		t.Fatalf("hop B timeout = %v, want %v", hopBTimeout, want)
	}

	// Hop B uses 300ms.
	advance(t, 300*time.Millisecond)

	reserve := 50 * time.Millisecond
	hopCTimeout, err := ChildTimeout(ctx, 2*time.Second, reserve)
	if err != nil {
		t.Fatalf("hop C: unexpected error: %v", err)
	}
	if want := 450 * time.Millisecond; hopCTimeout != want {
		t.Fatalf("hop C timeout = %v, want %v", hopCTimeout, want)
	}
}

// advance moves the package-level fake clock forward by d. It must be
// called after withFakeDeadline has installed the fake clock.
func advance(t *testing.T, d time.Duration) {
	t.Helper()
	cur := now()
	now = func() time.Time { return cur.Add(d) }
}

func TestCanAfford(t *testing.T) {
	ctx := withFakeDeadline(t, 150*time.Millisecond)

	if CanAfford(ctx, 400*time.Millisecond, 0) {
		t.Fatal("CanAfford() = true, want false: not enough budget for a 400ms operation with 150ms left")
	}
	if !CanAfford(ctx, 100*time.Millisecond, 40*time.Millisecond) {
		t.Fatal("CanAfford() = false, want true: 150ms - 40ms reserve should cover a 100ms operation")
	}
}

func TestRemaining_NoDeadline(t *testing.T) {
	if got := Remaining(context.Background()); got != -1 {
		t.Fatalf("Remaining() = %v, want -1 for a context with no deadline", got)
	}
}

func TestChildTimeout_NoDeadlineReturnsMax(t *testing.T) {
	got, err := ChildTimeout(context.Background(), 500*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 500*time.Millisecond {
		t.Fatalf("ChildTimeout() = %v, want 500ms", got)
	}
}
