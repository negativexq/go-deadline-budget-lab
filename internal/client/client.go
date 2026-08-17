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

// retryable reports whether an attempt's outcome should be retried. A
// transport-level error (connection refused, timeout, ...) is always
// retryable. A response that came back with no error is only retryable if
// it's a 429 or 5xx — those are the status codes a server uses to signal
// "try again," as opposed to a 4xx client error, which retrying can't fix.
func retryable(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if resp == nil {
		return false
	}
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
}

// GetWithRetry retries a request using delays as the backoff schedule
// between attempts, retrying both transport errors and 429/5xx responses.
// Before sleeping for each backoff delay, it checks whether the remaining
// budget can actually afford that delay plus another attempt; if not, it
// stops retrying immediately instead of sleeping into a doomed attempt.
//
// Every response except the one finally returned to the caller has its
// body closed here, so discarded intermediate attempts don't leak
// connections.
func (c *Client) GetWithRetry(ctx context.Context, url string, maxTimeout time.Duration, delays []time.Duration) (*http.Response, error) {
	resp, err := c.Get(ctx, url, maxTimeout)
	if !retryable(resp, err) {
		return resp, err
	}
	if err == nil {
		resp.Body.Close()
	}
	lastResp, lastErr := resp, err

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
		if !retryable(resp, err) {
			return resp, err
		}
		if err == nil {
			resp.Body.Close()
		}
		lastResp, lastErr = resp, err
	}

	return lastResp, lastErr
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}
