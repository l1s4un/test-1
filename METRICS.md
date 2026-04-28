# METRICS.md — Metrics Design

## Catalog

```text
# Counters
orders_created_total{status="success|failure", customer_tier="free|premium"}
payments_total{status="captured|declined|error"}
outbox_published_total{event_type, status}

# Histograms
order_processing_duration_seconds{step="validation|payment|fulfillment"}
http_request_duration_seconds{route, method, status_class}     # 2xx/4xx/5xx, NOT raw status
db_query_duration_seconds{query_name}                          # named queries only

# Gauges
orders_pending_count{}
outbox_lag_seconds{}            # max(now() - created_at) for unprocessed rows
inflight_requests{route}
```

Buckets: SLO-aligned. For `order_processing_duration_seconds` we target a P99 of 500 ms, so buckets cluster around that: `[5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s, 10s]`.

---

## Q1. Where do you instrument?

**At the use-case boundary, via the `MeteredExecutor` decorator** — same reasoning as TRACING and LOGGING. That gives us:

- `usecase_total{operation, status}`
- `usecase_duration_seconds{operation, status}`

**Plus narrowly-scoped instrumentation in two specific places:**

- **Repositories:** `db_query_duration_seconds{query_name}`. SQL latency
  is a distinct concern from use-case latency and helps diagnose
  "use-case slow → which query?" without having to read traces.
- **External clients:** `http_client_duration_seconds{target, status_class}`
  for payment/inventory calls, so a Payment slowdown is visible without
  parsing `usecase_duration_seconds` for hidden tail latency.

**NOT instrumented:** individual domain methods. Spans/events cover those with no cardinality cost.

## Q2. Why does this explode?

```go
orderDuration.WithLabelValues(customerID, productID, orderID).Observe(duration)
```

`customerID`, `productID`, `orderID` are **unbounded high-cardinality**. Each unique combination creates a new time-series. With 1M customers × 10k products × 10M orders the cardinality is astronomic — Prometheus will OOM, ingestion costs explode, and queries become unusable.

**Fix — bounded labels only:**

```go
// Replace IDs with low-cardinality categorizations.
orderDuration.WithLabelValues(
    customerTier,    // "free" | "premium" | "enterprise" (≤5)
    productCategory, // "physical" | "digital" | "subscription" (≤10)
    statusClass,     // "success" | "client_error" | "server_error"
).Observe(duration)
```

For per-entity investigation use **traces and logs** (which are joined to metrics by `trace_id` exemplars), not metric labels.

In this repo's code that mistake is structurally prevented: `InMemoryMeter.Counter("name", labelKeys...)` registers the allowed label keys at construction time. `labelKey()` iterates **the registered keys only**, silently dropping unregistered ones — see `TestInMemoryMeter_DropsUnregisteredLabels`.

## Q3. How do you correlate metrics with traces?

**Exemplars (OpenMetrics / OTel).** When recording a histogram observation, attach the active `trace_id` as an exemplar:

```go
hist.ObserveWithExemplar(duration, observability.Labels{"trace_id": span.TraceID()})
```

In Grafana / Prometheus, hovering a histogram bucket shows a clickable trace ID that opens the matching span in Tempo/Jaeger. This gives you the "slow P99 → which exact request?" workflow without having to grep logs.

If exemplars aren't available, the cheaper alternative is to **log at the SLO boundary**: every request that exceeds the P99 budget emits an `INFO` log with `trace_id`, route, and duration; alerting then queries that stream.

---

## Implementation

See `observability/metrics.go` and `MeteredExecutor` in `observability/decorators.go`. Replace `InMemoryMeter` with a Prometheus or OTel SDK adapter at the composition root; the use-case layer is unchanged.