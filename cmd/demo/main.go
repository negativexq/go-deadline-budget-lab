// Command demo shows a request's deadline budget being divided across two
// simulated downstream hops (a database and an upstream API), instead of
// each hop picking its own fixed timeout.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/negativexq/go-deadline-budget-lab/internal/budget"
	"github.com/negativexq/go-deadline-budget-lab/internal/client"
	"github.com/negativexq/go-deadline-budget-lab/internal/fixture"
)

func main() {
	var (
		totalBudget   = flag.Duration("budget", time.Second, "total end-to-end request budget")
		reserve       = flag.Duration("reserve", 100*time.Millisecond, "time reserved after every hop for the parent to finish cleanly")
		processing    = flag.Duration("processing", 120*time.Millisecond, "time spent in local handler logic before the first hop")
		dbMax         = flag.Duration("db-max", 500*time.Millisecond, "configured max timeout for the database hop")
		dbDelay       = flag.Duration("db-delay", 420*time.Millisecond, "simulated database response time")
		upstreamMax   = flag.Duration("upstream-max", 800*time.Millisecond, "configured max timeout for the upstream hop")
		upstreamDelay = flag.Duration("upstream-delay", 300*time.Millisecond, "simulated upstream response time")
	)
	flag.Parse()

	srv := fixture.NewServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *totalBudget)
	defer cancel()

	fmt.Printf("request_budget=%s\n", *totalBudget)
	fmt.Printf("reserve=%s\n\n", *reserve)

	time.Sleep(*processing)
	fmt.Printf("hop=processing\nelapsed=%s\nremaining=%s\n\n", *processing, budget.Remaining(ctx))

	c := &client.Client{HTTP: http.DefaultClient, Reserve: *reserve}

	if err := runHop(ctx, c, "database", srv.URL, "/slow", *dbMax, *dbDelay); err != nil {
		report(err)
		return
	}

	if err := runHop(ctx, c, "upstream", srv.URL, "/slow", *upstreamMax, *upstreamDelay); err != nil {
		report(err)
		return
	}

	fmt.Println("result=success")
}

func runHop(ctx context.Context, c *client.Client, name, base, path string, maxTimeout, delay time.Duration) error {
	remaining := budget.Remaining(ctx)
	allocated, err := budget.ChildTimeout(ctx, maxTimeout, c.Reserve)

	fmt.Printf("hop=%s\nconfigured_max=%s\nremaining=%s\n", name, maxTimeout, remaining)
	if err != nil {
		fmt.Printf("allocated=0\nresult=%s\n\n", err)
		return err
	}
	fmt.Printf("allocated=%s\n", allocated)

	start := time.Now()
	resp, err := c.Get(ctx, fmt.Sprintf("%s%s?delay=%s", base, path, delay), maxTimeout)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Printf("elapsed=%s\nresult=%s\n\n", elapsed, classify(err))
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	fmt.Printf("elapsed=%s\nresult=ok\n\n", elapsed)
	return nil
}

func classify(err error) string {
	switch {
	case errors.Is(err, budget.ErrBudgetExhausted):
		return "budget_exhausted"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return err.Error()
	}
}

func report(err error) {
	fmt.Fprintf(os.Stderr, "request failed: %v\n", err)
	os.Exit(1)
}
