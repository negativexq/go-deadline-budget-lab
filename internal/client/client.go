// Package client shows how a downstream call site consumes the shared
// deadline budget instead of choosing its own fixed timeout.
package client

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/negativexq/go-deadline-budget-lab/internal/budget"
)

// Client wraps an *http.Client so every call is bounded by the caller's
// remaining deadline budget, never by a timeout picked in isolation.
type Client struct {
	HTTP    *http.Client
	Reserve time.Duration
}

// Get performs a GET request whose timeout is min(maxTimeout, remaining
// budget - Reserve). If there isn't enough budget left to even attempt the
// call, it returns budget.ErrBudgetExhausted without making a request.
func (c *Client) Get(ctx context.Context, url string, maxTimeout time.Duration) (*http.Response, error) {
	timeout, err := budget.ChildTimeout(ctx, maxTimeout, c.Reserve)
	if err != nil {
		return nil, err
	}

	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(childCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	return c.httpClient().Do(req)
}

// GetWithRetry retries a GET request using delays as the backoff schedule
// between attempts. Before sleeping for each backoff delay, it checks
// whether the remaining budget can actually afford that delay plus another
// attempt; if not, it stops retrying immediately instead of sleeping into a
// doomed attempt.
func (c *Client) GetWithRetry(ctx context.Context, url string, maxTimeout time.Duration, delays []time.Duration) (*http.Response, error) {
	resp, err := c.Get(ctx, url, maxTimeout)
	if err == nil {
		return resp, nil
	}
	lastErr := err

	for _, delay := range delays {
		if !budget.CanAfford(ctx, delay+maxTimeout, c.Reserve) {
			return nil, fmt.Errorf("%w: insufficient budget for retry after %s backoff", budget.ErrBudgetExhausted, delay)
		}

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}

		resp, err = c.Get(ctx, url, maxTimeout)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}

	return nil, lastErr
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
