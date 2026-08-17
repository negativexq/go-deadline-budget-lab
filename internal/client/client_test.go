package client_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/negativexq/go-deadline-budget-lab/internal/budget"
	"github.com/negativexq/go-deadline-budget-lab/internal/client"
	"github.com/negativexq/go-deadline-budget-lab/internal/fixture"
)

func TestGet_SucceedsWithinBudget(t *testing.T) {
	srv := fixture.NewServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	c := &client.Client{Reserve: 20 * time.Millisecond}
	resp, err := c.Get(ctx, srv.URL+"/slow?delay=10ms", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()

	if got := srv.Count("/slow"); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

// Star test 2 (client-level): when the remaining budget can't even cover
// the reserve, the downstream service must never be called.
func TestGet_BudgetExhausted_NeverCallsDownstream(t *testing.T) {
	srv := fixture.NewServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	time.Sleep(15 * time.Millisecond) // let the parent deadline actually pass

	c := &client.Client{Reserve: 50 * time.Millisecond}
	_, err := c.Get(ctx, srv.URL+"/slow?delay=1ms", 2*time.Second)

	if !errors.Is(err, budget.ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	if got := srv.Count("/slow"); got != 0 {
		t.Fatalf("request count = %d, want 0 (downstream should never be called)", got)
	}
}

// The child call must never outlive the parent deadline: a downstream that
// takes longer than the allocated child timeout should be cut off by
// context.DeadlineExceeded, not allowed to run to completion.
func TestGet_ChildNeverOutlivesParentDeadline(t *testing.T) {
	srv := fixture.NewServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	c := &client.Client{Reserve: 10 * time.Millisecond}
	start := time.Now()
	_, err := c.Get(ctx, srv.URL+"/slow?delay=500ms", 2*time.Second)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("elapsed = %v, expected the call to be cut off near the parent deadline (60ms), not run to 500ms", elapsed)
	}
}

// Star test 5: budget-aware retry must stop before sleeping into an
// attempt it can't possibly complete in time.
func TestGetWithRetry_StopsWhenBudgetInsufficient(t *testing.T) {
	srv := fixture.NewServer()
	defer srv.Close()

	// The first attempt always fails fast (downstream errors immediately
	// by refusing the connection at a bad URL), leaving ~150ms of a 200ms
	// total budget. The next retry delay (400ms) can't fit.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	c := &client.Client{Reserve: 10 * time.Millisecond}
	delays := []time.Duration{400 * time.Millisecond}

	start := time.Now()
	_, err := c.GetWithRetry(ctx, "http://127.0.0.1:1/unreachable", 50*time.Millisecond, delays)
	elapsed := time.Since(start)

	if !errors.Is(err, budget.ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("elapsed = %v, retry should have been skipped instead of sleeping 400ms", elapsed)
	}
}

// A 503 is a successful HTTP round trip as far as net/http is concerned
// (err == nil), so GetWithRetry must inspect the status code itself to
// know a retry is warranted.
func TestGetWithRetry_RetriesOn5xx(t *testing.T) {
	srv := fixture.NewServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &client.Client{Reserve: 10 * time.Millisecond}
	delays := []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}

	resp, err := c.GetWithRetry(ctx, srv.URL+"/flaky?fails=2&status=503", 200*time.Millisecond, delays)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := srv.Count("/flaky"); got != 3 {
		t.Fatalf("request count = %d, want 3 (1 initial + 2 retries)", got)
	}
}

// A 404 is not retryable: retrying it can't turn it into success, so
// GetWithRetry must return it immediately without spending any retries.
func TestGetWithRetry_DoesNotRetryOn4xx(t *testing.T) {
	srv := fixture.NewServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &client.Client{Reserve: 10 * time.Millisecond}
	delays := []time.Duration{10 * time.Millisecond}

	resp, err := c.GetWithRetry(ctx, srv.URL+"/flaky?fails=5&status=404", 200*time.Millisecond, delays)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := srv.Count("/flaky"); got != 1 {
		t.Fatalf("request count = %d, want 1 (no retries on a 4xx)", got)
	}
}
