# go-deadline-budget-lab

**Time is a resource.**

Retries consume time. Queues consume time. Downstream calls consume time.
Without a shared deadline, individually reasonable timeouts can exceed the
caller's end-to-end latency budget.

A request arrives with a total time budget — say, 2 seconds. The system
makes several downstream calls: `API → Service A → Service B → DB`. If each
hop picks its own 2 second timeout "just to be safe," the request can take
6 seconds in the worst case. The caller's SLO is broken before any single
hop has done anything wrong.

The fix is to carry one end-to-end deadline through the whole call chain and
let each hop spend only what's left of it:

```
2s request budget

API          uses 200ms
  ↓ 1.8s left
Service A    uses 450ms
  ↓ 1.35s left
Service B    uses 600ms
  ↓ 750ms left
DB           gets at most 650ms (750ms - 100ms reserve)
```

If the next hop wants 800ms and there's only 450ms of usable budget left,
it should get a shorter timeout — or not start at all.

This repo is a small, dependency-free demonstration of that idea.

## Deadline vs. timeout

These sound interchangeable but aren't:

- **Timeout**: "This operation should take at most 500ms."
- **Deadline**: "Everything about this request must be done by 14:32:05.500."

A timeout is local and relative — every hop that reasons only in timeouts is
making an independent, uncoordinated decision. A deadline is a single point
in time that can be handed from hop to hop, so the whole chain shares one
notion of "how much time is actually left."

Why can't every hop just use the same fixed timeout?

```
Request
  ↓ 500ms timeout
Service A
  ↓ 500ms timeout
Service B
  ↓ 500ms timeout
DB
```

Worst case, that's 1500ms+ — the caller's 500ms SLO is gone. With deadline
propagation, the *total* stays 500ms and each hop gets whatever's left after
the previous ones:

```
total request budget = 500ms

A uses 100ms  → B gets ~400ms
B uses 150ms  → DB gets ~250ms
```

## Core abstraction

```go
func Remaining(ctx context.Context) time.Duration
```

Reads `ctx.Deadline()` and returns how much time is left.

```go
func ChildTimeout(
    ctx context.Context,
    maxTimeout time.Duration,
    reserve time.Duration,
) (time.Duration, error)
```

```
usable = remaining(ctx) - reserve

if usable <= 0:
    return ErrBudgetExhausted

timeout = min(maxTimeout, usable)
```

Example: a parent has 300ms left, `reserve` is 50ms, and the caller asked
for an 800ms max timeout for the next hop:

```go
timeout, err := budget.ChildTimeout(ctx, 800*time.Millisecond, 50*time.Millisecond)
// timeout == 250ms
```

The child can never outlive the parent: `childDeadline <= parentDeadline`,
always.

### Why `reserve` exists

If a child is handed *all* of the remaining time, it can time out at exactly
the moment the parent's own deadline expires:

```
remaining = 500ms
↓
child gets all 500ms
↓
child times out at exactly 500ms
↓
parent has 0ms left
↓
cannot serialize response, cannot log, cannot clean up
```

`reserve` withholds a slice of the budget so the parent always has a little
room to finish gracefully after the child returns:

```
remaining = 500ms
reserve   = 50ms
child budget = 450ms max
```

### Error taxonomy

- `budget.ErrBudgetExhausted` — there wasn't enough budget to even *attempt*
  the operation. It fails fast, before any network call.
- `context.DeadlineExceeded` — the operation was attempted and started, but
  ran out of time while in flight.

The distinction matters: the first means "don't bother," the second means
"we tried and ran out of time."

## Package layout

```
cmd/demo/          runnable demo: two simulated hops sharing one deadline
internal/budget/   the core Remaining / ChildTimeout / CanAfford logic
internal/client/   an HTTP client that derives its timeout from ctx budget
internal/fixture/  an in-process fake downstream service for tests/demo
```

No framework, no Redis, no database — just the standard library.

## Try it

```bash
make demo
```

Sample output:

```
request_budget=1s
reserve=100ms

hop=processing
elapsed=120ms
remaining=879ms

hop=database
configured_max=500ms
remaining=878ms
allocated=500ms
elapsed=424ms
result=ok

hop=upstream
configured_max=800ms
remaining=454ms
allocated=354ms
elapsed=302ms
result=ok

result=success
```

Tighten the budget or slow a hop down and watch a later hop get a shrunk
timeout, or fail fast with `budget_exhausted` before ever calling out:

```bash
go run ./cmd/demo -budget=900ms -upstream-delay=800ms
```

## Tests worth reading

- `ChildTimeout` never returns a timeout larger than the parent's remaining
  budget, even when `maxTimeout` is huge.
- When the reserve alone exceeds what's left, the downstream call is never
  made — `ErrBudgetExhausted`, and the fixture server's request count stays
  at zero.
- A generous remaining budget doesn't override a hop's own configured max
  timeout.
- Budget math is verified across multiple simulated hops off one deadline,
  using an injectable clock (`internal/budget`'s test-only `now` override)
  so the assertions are deterministic instead of depending on real sleeps.
- `client.GetWithRetry` retries transport errors and 429/5xx responses (a
  5xx is a successful round trip as far as `net/http` is concerned — `err`
  is `nil` — so the status code has to be checked explicitly; a 4xx is
  never retried). It checks `CanAfford` *before* sleeping into a backoff
  delay — if the remaining budget can't cover the next attempt, it returns
  `ErrBudgetExhausted` instead of sleeping into a doomed retry. Every
  discarded intermediate response has its body closed so retries don't
  leak connections.

## Engineering notes

**This connects retries, queues, and dependency calls under one question:**
*how much time do I still have the right to spend?*

- **Retries** — a fixed backoff schedule (100ms, 200ms, 400ms...) can blow
  through the caller's budget on its own. A retry loop that checks
  `CanAfford` before each sleep stops itself instead of sleeping into an
  attempt it can't complete.
- **Queueing / backpressure** — a bounded queue prevents memory growth, but
  it can still accept work that is already guaranteed to miss its caller's
  deadline. Deadline-aware admission (checking `Remaining(ctx)` against an
  estimated queue wait before enqueueing) can reject such work earlier,
  rather than doing it and then failing on the way out.
- **Circuit breakers** — "should I call this dependency" and "do I have
  enough budget left for it to possibly succeed" are two different
  questions; a breaker only answers the first one.

This repo deliberately doesn't implement a queue, a retry package, or a
circuit breaker — those are separate concerns. What it shows is the one
piece of context (a shared, shrinking time budget) that all of them need to
answer their own version of "should I still do this?"

## Scope

Deliberately excluded: OpenTelemetry, gRPC, service mesh, Envoy, Redis, a
real database, Kafka, distributed tracing, adaptive/ML-based timeouts. The
value here is in the budgeting logic being small enough to read in one
sitting.
