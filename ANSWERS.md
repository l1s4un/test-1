# ANSWERS.md

References to specific files in this repo are inlined; nothing here is generic textbook content.

---

## Q1. Tracing domain operations without breaking purity

The naïve approach — `func (o *Order) Complete(ctx context.Context) { span := trace.SpanFromContext(ctx); span.AddEvent(...) }` — drags `go.opentelemetry.io/otel/trace` into the domain package. Two practical fixes:

### Approach A — Surrounding span at the boundary

The use-case opens a span around the domain call. The domain method stays with its original signature `func (o *Order) Complete() error`.

```go
// usecase/complete_order.go
ctx, span := uc.tracer.Start(ctx, "domain.Order.Complete",
    observability.Int64("order_id", int64(o.ID())),
)
err := o.Complete()  // pure
span.End()
if err != nil {
    span.RecordError(err)
}
```

The trade-off: the span is "all or nothing" — internal steps inside `Complete()` are invisible. That's usually fine because well-factored domain methods are short.

### Approach B — Domain events as the source of truth

`Complete()` raises `OrderCompleted`, `PaymentRequested`, etc. The dispatcher (in the use-case layer) emits one span per event:

```go
for _, ev := range o.Events() {
    _, s := uc.tracer.Start(ctx, "domain.event."+ev.Name(),
        observability.Int64("aggregate_id", int64(ev.AggregateID())),
    )
    s.End()
}
```

This gives one span per business fact, which is what you actually want to filter on in Tempo/Jaeger. The domain still has zero observability imports.

In this repo's `observability/decorators.go` we use Approach A for use-cases via `TracedExecutor`. Approach B would be added as a `TracedDispatcher` decorator over the event publisher.

---

## Q2. Mocking decisions

### When mocking is the RIGHT choice

Use-case unit tests where you want to assert orchestration. Example: in `CompleteOrderInteractor`, assert that on a `repo.Get` "not found" error the use-case returns `ErrOrderNotFound` and **does not** call `repo.Save`. A mock with strict expectations on `Save` proves this in microseconds. A real DB would still pass even if you accidentally wrote to it on the error path, masking the bug.

### When mocking HIDES bugs (specific bug)

Mock the repository's `Save(ctx, o *Order) error` and assert the use-case calls it. The bug it hides: the underlying SQL builds an `INSERT INTO orders (id, customer_id, amount_cents, status, created_at)` — **forgetting `updated_at`**. The mock test passes (it never sees real SQL). Postgres rejects the INSERT with `null value in column "updated_at"`. A user files a bug after deploy.

The integration test `TestOrderRepo_CreateAndRead` would have caught it.

A second concrete example: an outbox writer that does `event.(*domain.OrderCompleted).ToJSON()` — a hard type assertion. A mock test passes. Production passes a different event type → panic. A test that exercises the real outbox writer with a non-Completed event catches it.

### When you need BOTH

Always for repositories. The unit test asserts "use-case calls Save once on success", the integration test asserts "Save actually persists the right columns and respects the unique constraint". They answer different questions; neither is sufficient alone.

---

## Q3. "Order charged but shows as failed" — debugging workflow

This is the classic dual-write / outbox scenario.

**Logs to look at, in order:**

1. `payments.captured` business log filtered by `order_id=X` —
   confirms the charge actually completed at the provider.
2. Application error logs joined by `trace_id` (provided by
   `SlogLogger.log()` automatically) for the failed `CreateOrder` request —
   look for `db error` or `commit failed` AFTER the payment client
   returned 200.
3. Outbox table query: `SELECT * FROM outbox WHERE aggregate_id=X` — if
   no row, the dual-write failed AFTER calling Payment but BEFORE the DB
   commit (the canonical bug).

**Metrics that indicate the problem:**

- `payments_total{status="captured"}` − `orders_created_total{status="success"}`
  diverging — payments ahead of orders means dual-write is leaking.
- `outbox_lag_seconds` spiking — outbox writer fell behind, customer
  hasn't seen the "completed" email yet.
- `usecase_duration_seconds{operation="CreateOrder", status="failure"}`
  histogram showing high latency near the payment step.

**Alerts:**

| Alert                          | Threshold                                                                                         | Severity |
| ------------------------------ | ------------------------------------------------------------------------------------------------- | -------- |
| Payments-vs-orders skew        | `rate(payments_total{status="captured"}[5m]) - rate(orders_created_total{status="success"}[5m]) > 0.01` for 10m | P1       |
| Outbox lag                     | `outbox_lag_seconds > 60` for 5m                                                                  | P2       |
| CreateOrder error rate         | `error_rate > 1%` for 5m                                                                          | P2       |

The fix is the outbox pattern — write the order row and the outbox row in the **same transaction**, never call Payment between them. See `TestOutbox_AtomicWrite` in `testing/integration/order_repo_test.go`.

---

## Q4. Testing the outbox end-to-end without flaky timing

The naïve test (`time.Sleep(2 * time.Second)` then assert the message arrived on Kafka) is flaky on slow CI runners and slow when fast.

**Strategy: deterministic time + synchronous tick + bounded wait.**

1. **Inject a `Clock` and a `Ticker`-shaped scheduler.** The outbox
   worker uses `clock.Now()` for `WHERE created_at < $1` queries (see
   `observability.Clock`). Tests use `FakeClock` (`observability/clock.go`).
2. **Expose a synchronous `RunOnce(ctx)` method on the worker.** The
   production loop is `for { select { case <-ticker.C: w.RunOnce(ctx) } }`,
   the test calls `w.RunOnce(ctx)` directly — no sleeps, no goroutines.
3. **Use a real broker via testcontainers (Kafka or Redpanda).** This is
   the part that mocks WOULD hide: the message format, partitioning,
   acks. Then `consumer.Poll(ctx, 5*time.Second)` is a *bounded* wait,
   not a sleep.
4. **`assert.Eventually(t, predicate, 5s, 50ms)`** for the rare case
   where you can't avoid asynchrony — it succeeds the millisecond the
   condition becomes true and only fails after the timeout.
5. **Property: idempotency.** Run `RunOnce` twice and assert exactly one
   message is delivered (the worker marks `processed_at` inside the same
   transaction that publishes — or, when using transactional Kafka,
   commits both atomically).

```go
db := setup.Postgres(t, schema)
broker := setup.Redpanda(t)            // hypothetical
clock := observability.NewFakeClock(t0)
worker := outbox.New(db, broker, clock)

mustExec(t, db, "INSERT INTO outbox(aggregate_id, event_type, payload) VALUES (1,'OrderCompleted','{}')")

if err := worker.RunOnce(ctx); err != nil {
	t.Fatal(err)
}

msg := broker.MustPoll(t, 5*time.Second)
require.Equal(t, "OrderCompleted", msg.Header("event_type"))

// Idempotency:
if err := worker.RunOnce(ctx); err != nil {
	t.Fatal(err)
}
broker.MustReceiveNothing(t, 200*time.Millisecond)
```

---

## Q5. `time.Now()` in tests fails in CI

```go
order.CreatedAt = time.Now()
// ... later
assert.Equal(t, time.Now(), order.CreatedAt)  // flaky
```

**What's wrong:** the two `time.Now()` calls return different instants — nanoseconds apart locally, sometimes milliseconds apart on a loaded CI runner. `time.Time` comparison is exact, including monotonic clock, location, and nanosecond precision.

**The fix has three parts, all already implemented in this repo:**

1. **Inject a `Clock` interface, never call `time.Now()` directly.**
   `observability.Clock` (`observability/clock.go`) has `RealClock`,
   `FixedClock`, and `FakeClock`. The aggregate constructor takes a
   `time.Time` rather than calling `time.Now()` itself:

   ```go
   func NewOrder(id OrderID, customer CustomerID, now time.Time) *Order
   ```

   The use-case supplies the time from the injected clock — that's the
   only place a real wall-clock value enters the system.

2. **Use a `FixedClock` in tests.**

   ```go
   t0 := time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC)
   clock := observability.FixedClock{T: t0}
   uc := usecase.NewInteractor(repo, clock)
   ...
   require.Equal(t, t0, order.CreatedAt())
   ```

3. **For monotonic / elapsed-time assertions, use a `FakeClock` with
   explicit advances.** `FakeClock.Advance(time.Hour)` is deterministic.
   See `TestFakeClock_Deterministic` in `observability_test.go`.

Bonus rules to make CI stable:

- Strip the monotonic component on persisted timestamps:
  `t.Round(0)` before serializing. Postgres has no monotonic component;
  comparing a Go time with one read back from Postgres can fail without it.
- Compare with tolerance ONLY when neither side is under your control:
  `assert.WithinDuration(t, expected, actual, time.Second)`.
- Use UTC everywhere. `time.Now().UTC()` in production code, `time.Date(..., time.UTC)`
  in tests. Eliminates DST and `time.Local` headaches in CI containers.