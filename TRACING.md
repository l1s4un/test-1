# TRACING.md — Trace Context Propagation

## TL;DR

| Layer        | Mechanism                          | Why                                                             |
| ------------ | ---------------------------------- | --------------------------------------------------------------- |
| HTTP/gRPC    | OTel propagators (W3C `traceparent`) | Standard interop with API Gateway, Inventory, Payment.         |
| Use-case     | **Decorator** + `context.Context`   | Keeps the use-case free of tracing imports.                    |
| Domain       | **Domain events** (no ctx, no span) | Domain stays pure; spans are added when events are dispatched. |
| Repository   | `context.Context`                   | Already needed for cancellation/deadlines; spans are cheap.    |

The interactor itself never imports `go.opentelemetry.io/otel`. It depends only on `observability.Tracer`, the small interface defined in this repo.

---

## Q1. Which option for use-cases? Why?

**Option C — middleware/decorator wrapper.**

```go
inner   := usecase.NewInteractor(repo, clock)
traced  := observability.TracedExecutor[Req, Resp]{
    Inner:  inner,
    Tracer: tracer,
    Name:   "usecase.CreateOrder",
}
```

Reasons:

1. **Use-case body stays focused on business logic.** No `span.AddEvent`
   calls polluting the orchestration steps.
2. **Test parity.** The `TracedExecutor` is wired only at the composition
   root; unit tests run the bare interactor, which means tests don't have
   to know about tracing.
3. **Composability.** Logging, metrics and tracing decorators stack in any
   order; in this repo they are independent types so behavior is obvious
   from the wiring code.
4. **Option A still works through ctx** — the decorator places the span in
   `ctx`, so a use-case that *needs* to add a custom event (rare) can pull
   the span via `observability.SpanFromContext(ctx)` without importing OTel.

Option B (explicit `TraceContext` parameter) is rejected: it forces every caller to manually build a trace context and breaks Go's idiom that ctx carries cross-cutting values.

## Q2. Which option for domain methods? Why?

**None of the above. Domain methods take NO ctx and NO tracer.**

A domain method like `Order.Complete()` returns a result and may raise domain events. Tracing is added when those events are *dispatched*:

```go
// In the use-case, after order.Complete() raised events:
for _, ev := range order.Events() {
    _, span := tracer.Start(ctx, "domain.event."+ev.Name())
    span.SetAttributes(observability.Int64("order_id", int64(ev.AggregateID())))
    span.End()
}
```

This preserves the rule that the Order aggregate has no `context.Context`, no logging, and no DB imports — trace points are modelled explicitly as domain events.

## Q3. Which option for repository methods?

**Option A — `context.Context`.** Repos already need ctx for connection deadlines and cancellation; the OTel SQL/HTTP instrumentation hooks (or a thin manual span around each query) read the parent span from ctx automatically. The repo interface in the use-case package stays:

```go
type OrderRepo interface {
    Save(ctx context.Context, o *domain.Order) error
    Get(ctx context.Context, id domain.OrderID) (*domain.Order, error)
}
```

## Q4. How do you trace a domain method without passing context to it?

Two complementary techniques (also covered in ANSWERS.md Q1):

1. **Surrounding span.** The use-case opens a span around the call:

   ```go
   ctx, span := tracer.Start(ctx, "domain.Order.Complete")
   err := order.Complete()
   span.End()
   ```

   The domain method is unaware; observability is added at the boundary.

2. **Domain events.** `Order.Complete()` raises `OrderCompleted`. The
   event dispatcher emits a span per event. This gives one span per
   business fact, not per code call — usually more useful.

---

## Implementation

See `observability/tracing.go` (`Tracer`, `Span`, `InMemoryTracer`, `SpanFromContext`) and `observability/decorators.go` (`TracedExecutor`).

For production, swap `InMemoryTracer` with an OTel adapter — the use-case doesn't change.