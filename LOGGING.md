# LOGGING.md — Structured Logging Strategy

## Q1. Which option maintains Clean Architecture?

**Option C — Decorator (`LoggedExecutor`).**

```go
inner  := usecase.NewInteractor(...)
logged := observability.LoggedExecutor[Req, Resp]{Inner: inner, Log: log, Name: "CreateOrder"}
```

- Option A puts a logger field on the interactor: every use-case test must
  now construct a logger; `nil` panics; SRP is violated.
- Option B logs only at the service layer — fine for boundary I/O logs but
  cannot capture step-level business events without leaking back into the
  use-case.
- Option C keeps the use-case pure AND lets us add per-step logs as
  additional decorators where genuinely needed (e.g. an `AuditLogger` that
  reads domain events and emits an audit-trail entry per event).

The use-case in this repo depends on `observability.Logger` — a 5-method interface — never on `slog`/`zap` directly. That keeps the dependency arrows pointing inward.

## Q2. Business logs vs operational logs

| Type            | Source                  | Audience              | Retention | Examples                                                |
| --------------- | ----------------------- | --------------------- | --------- | ------------------------------------------------------- |
| **Business**    | Domain events, use-case | Product, finance, ops | Long      | "order.completed", "payment.captured", "refund.issued"  |
| **Operational** | Infra, middleware       | SRE / on-call         | Short     | "db connection lost", "HTTP 5xx from /v1/charges", "GC pause" |

Implications for design:

- Business logs are derived from **domain events** (deterministic, auditable),
  not free-text log statements scattered through code.
- Operational logs use levels `WARN`/`ERROR` and are paired with metrics +
  traces, not used as a primary metrics source ("logs as metrics" is a
  cardinality nightmare).
- Business logs may be shipped to a long-retention store (S3 + Athena),
  operational logs to ELK/Loki with 7–30 day retention.

## Q3. How do you avoid logging sensitive data?

Three layers, defense in depth:

1. **Type-level prevention.** Use value objects: `Email`, `CardNumber`,
   `SSN` types whose `String()` returns `[REDACTED]` and whose `MarshalJSON`
   does the same. The compiler then makes leaks impossible at the call
   site without an explicit unmask.
2. **Logger-level redaction.** `SlogLogger` accepts a `PIIKeys` allowlist
   (`email`, `card_number`, `password`, `phone`, `address_line`, ...). Any
   attribute with a matching key is replaced with `[REDACTED]` before
   emission. See `observability/logging.go` and the test
   `TestSlogLogger_RedactsPII`.
3. **Pipeline-level scrubbing.** A Vector/Fluent Bit transform regex-scrubs
   anything that *looks* like a card number / JWT before it leaves the
   node — covers cases where a third-party library logs unexpectedly.

Never log: raw request bodies (esp. POST /charges), auth headers, JWTs, session cookies, full names + DoB together (correlated PII), IPs in GDPR-regulated contexts beyond the legal retention window.

---

## Implementation features

`observability/logging.go` implements `SlogLogger` with:

| Feature                                   | Where                                                 |
| ----------------------------------------- | ----------------------------------------------------- |
| Trace ID & span ID injected per log line  | `SlogLogger.log()` → `SpanFromContext(ctx)`           |
| Request/response logging at boundaries    | `LoggedExecutor` decorator                            |
| Error logs include stack traces           | `SlogLogger.Error()` → `runtime/debug.Stack()`        |
| PII redaction                             | `SlogConfig.PIIKeys`                                  |
| Structured JSON output                    | `slog.NewJSONHandler`                                 |
| Service-wide attributes (service, env)    | `slog.Logger.With` at construction                    |

Sample log line:

```json
{
  "time":"2026-04-27T08:14:11Z",
  "level":"INFO",
  "msg":"CreateOrder completed",
  "service":"orders",
  "trace_id":"f3c9...",
  "span_id":"a812...",
  "customer_tier":"premium"
}
```