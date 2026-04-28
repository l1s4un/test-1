# TESTING.md — Testing Strategy

## Test Pyramid

| Level         | What to test                                                                                                                        | What to mock                                                              | Speed     | Where                          |
| ------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- | --------- | ------------------------------ |
| **Unit**      | Pure domain logic (invariants, state transitions, value-object math). Use-case orchestration logic.                                  | Repositories, external clients, clock, message queue, ID generator.       | <1 ms     | `*_test.go` next to source     |
| **Integration** | Repository SQL against real Postgres. HTTP clients against WireMock. Outbox poller against real DB. Migrations forward & backward. | Nothing inside our process; only the **outside world** (3rd-party APIs).  | 5–30 s    | `testing/integration/` (`-tags=integration`) |
| **E2E**       | Critical user journeys end-to-end (CreateOrder → Payment → Inventory → OrderCompleted event arrives on Kafka).                       | Only truly external dependencies you can't run (e.g. Stripe live).        | 1–5 min   | separate repo / nightly suite  |
| **Contract**  | Provider/consumer contract per integration partner.                                                                                  | Pact broker stubs.                                                         | <30 s     | `testing/contract/`            |

### Sizing

Target ratios (rough): **70% unit / 20% integration / 8% contract / 2% E2E**. Unit tests are the safety net for refactoring; integration tests are the safety net for *real-world* bugs that mocks miss; E2E proves the whole system breathes.

---

## What mocks DO and DON'T catch

A `mockRepo.On("Save", ...).Return(...)`-style test gives decent scenario coverage but cannot catch:

| Bug class                                          | Detected by mock test? | Detected by integration test? |
| -------------------------------------------------- | ---------------------- | ----------------------------- |
| Missing field in INSERT (`amount_cents`)           | ❌                     | ✅ (constraint violation / null) |
| Wrong column type (text instead of bigint)         | ❌                     | ✅                            |
| Forgotten `WHERE id = $1`                          | ❌                     | ✅ (test sees other row mutated) |
| JSON tag mismatch with upstream API                | ❌                     | ✅ (WireMock matches body)    |
| Idempotency-Key header missing                     | ❌                     | ✅ (WireMock matches headers) |
| Time zone serialization                            | ❌                     | ✅                            |
| Optimistic-lock version conflict handling          | ❌                     | ✅                            |
| Domain invariant: amount must be ≥0                | ✅                     | ✅                            |

→ **Use mocks to assert the use-case ORCHESTRATES correctly. Use integration tests to prove the use-case PERSISTS / TALKS correctly.**

---

## Integration testing tools used

- **testcontainers-go** for Postgres — gives a fresh, isolated database per
  test package without polluting a developer's machine. See
  `testing/setup/testcontainers.go::Postgres`.
- **WireMock** for external HTTP — lets us stub status codes, latencies,
  body matching by header. Implemented via a generic container in
  `testing/setup/testcontainers.go::WireMockEndpoint` and used in
  `testing/integration/payment_client_test.go`.
- **Build tag `integration`** keeps the default `go test ./...` fast and
  Docker-free for everyday development.

```bash
# fast feedback loop
go test ./...

# full integration suite (Docker required)
go test -tags=integration ./testing/integration/...
```

---

## Concrete tests in this repo

### Repository (Postgres testcontainer)

`testing/integration/order_repo_test.go`:

- **`TestOrderRepo_CreateAndRead`** — schema and round-trip.
- **`TestOrderRepo_AmountCheckConstraint`** — proves the DB-level CHECK
  catches negative amounts (a defense in depth alongside the domain check).
- **`TestOrderRepo_UpdateOnlyDirtyFields`** — proves the partial-update
  path writes exactly the dirty columns AND that the optimistic-lock
  version bumps. Reading the row back and asserting the *untouched*
  column equals its original value is what catches a "forgot WHERE
  clause" bug.
- **`TestOutbox_AtomicWrite`** — proves the aggregate INSERT and the
  outbox INSERT live in the same transaction (rollback removes both),
  closing the dual-write hole.

### External payment service (WireMock)

`testing/integration/payment_client_test.go`:

- **Success** — stubs `POST /v1/charges` → 200, asserts the client parses.
- **5xx** — stubs 503, asserts the client returns a structured error.
- **Timeout** — stubs `fixedDelayMilliseconds: 2000` against a 200 ms
  client timeout, asserts ctx cancellation propagates.

Each test stubs **header-aware** matchers (`Idempotency-Key: { matches: ".+" }`), so a regression where we forget the header is caught immediately.

---

## Determinism

Calling `time.Now()` directly inside aggregate constructors makes tests flaky. Every test in this repo uses `observability.FixedClock` or `FakeClock` — see `observability/clock.go` and ANSWERS.md Q5.

Other determinism rules:

- ID generator is an interface; tests inject a sequence-based fake.
- `t.Parallel()` is the default; shared state must go through
  per-test factories (especially testcontainers — see how each test gets
  its own `setup.Postgres(t, schema)` call).
- No reliance on map iteration order; `labelKey()` sorts keys.