// Package budget turns a context deadline into a spendable time budget for
// downstream calls, so a single end-to-end deadline can be divided across
// hops instead of each hop picking its own timeout.
package budget

import (
	"context"
	"errors"
	"time"
)

// ErrBudgetExhausted is returned when there is not enough remaining budget
// (after reserving headroom) to even attempt an operation. It is distinct
// from context.DeadlineExceeded: that error means an operation started and
// then ran out of time, while ErrBudgetExhausted means the operation was
// never allowed to start.
var ErrBudgetExhausted = errors.New("deadline budget exhausted")

// ErrInvalidBudgetConfig is returned when the caller's own inputs don't
// make sense as a budget configuration — e.g. a negative reserve, which
// would let a child be allocated more time than the parent actually has
// left. The parent context is still a hard ceiling a child can't exceed,
// but a negative reserve breaks the budgeting semantics regardless.
var ErrInvalidBudgetConfig = errors.New("invalid budget configuration")

// now is overridden in tests so budget math can be verified without
// depending on wall-clock sleeps.
var now = time.Now

// Remaining returns the time left before ctx's deadline. If ctx has no
// deadline, it returns -1, meaning "unbounded".
func Remaining(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return -1
	}
	return deadline.Sub(now())
}

// ChildTimeout computes the timeout a downstream call should use, given the
// caller's remaining deadline budget.
//
//	usable = remaining(ctx) - reserve
//	timeout = min(maxTimeout, usable)
//
// reserve is time deliberately withheld from the child so the parent can
// still serialize a response, log, and clean up after the child returns.
// If ctx has no deadline, maxTimeout is returned unchanged. If usable is
// zero or negative, ErrBudgetExhausted is returned and the caller should
// not attempt the operation at all. maxTimeout must be positive and
// reserve must not be negative; otherwise ErrInvalidBudgetConfig is
// returned.
func ChildTimeout(ctx context.Context, maxTimeout, reserve time.Duration) (time.Duration, error) {
	if maxTimeout <= 0 || reserve < 0 {
		return 0, ErrInvalidBudgetConfig
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		return maxTimeout, nil
	}

	usable := deadline.Sub(now()) - reserve
	if usable <= 0 {
		return 0, ErrBudgetExhausted
	}

	if maxTimeout < usable {
		return maxTimeout, nil
	}
	return usable, nil
}

// CanAfford reports whether ctx has enough remaining budget, after reserve,
// to cover operationTimeout. It's meant for call sites that need a yes/no
// answer before doing something costly (e.g. sleeping before a retry)
// rather than a computed timeout. An invalid configuration (a non-positive
// operationTimeout or a negative reserve) is treated as unaffordable.
func CanAfford(ctx context.Context, operationTimeout, reserve time.Duration) bool {
	if operationTimeout <= 0 || reserve < 0 {
		return false
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return deadline.Sub(now())-reserve >= operationTimeout
}
