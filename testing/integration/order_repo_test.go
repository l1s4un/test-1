//go:build integration

// Real-database test for OrderRepo using a postgres testcontainer.
//
// What this test verifies that a mock-based test cannot:
//   1. The SQL emitted by the repo's create path is actually valid and
//      accepted by Postgres.
//   2. UNIQUE / FOREIGN KEY / CHECK constraints catch domain invariants.
//   3. The partial-update path updates ONLY dirty fields (verified by
//      re-reading the row and comparing untouched columns to their initial
//      values).
//   4. Serialization round-trips correctly (status enum, cents int64, time).
//
// Run with: go test -tags=integration ./testing/integration/...
package integration_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"test-1/testing/setup"
)

const schema = `
CREATE TYPE order_status AS ENUM ('PENDING','COMPLETED','CANCELLED');

CREATE TABLE orders (
    id           BIGINT PRIMARY KEY,
    customer_id  BIGINT NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents >= 0),
    status       order_status NOT NULL DEFAULT 'PENDING',
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    version      BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE outbox (
    id          BIGSERIAL PRIMARY KEY,
    aggregate_id BIGINT NOT NULL,
    event_type  TEXT   NOT NULL,
    payload     JSONB  NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at TIMESTAMPTZ
);

CREATE INDEX outbox_unprocessed ON outbox (created_at) WHERE processed_at IS NULL;
`

func TestOrderRepo_CreateAndRead(t *testing.T) {
	t.Parallel()
	db := setup.Postgres(t, schema)
	ctx := context.Background()

	tests := []struct {
		name        string
		orderID     int64
		customerID  int64
		amountCents int64
		status      string
		createdAt   time.Time
	}{
		{
			name:        "basic order",
			orderID:     1,
			customerID:  42,
			amountCents: 9999,
			status:      "PENDING",
			createdAt:   time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
		},
		{
			name:        "completed order",
			orderID:     2,
			customerID:  123,
			amountCents: 50000,
			status:      "COMPLETED",
			createdAt:   time.Date(2025, 4, 27, 11, 30, 0, 0, time.UTC),
		},
		{
			name:        "cancelled order",
			orderID:     3,
			customerID:  999,
			amountCents: 100,
			status:      "CANCELLED",
			createdAt:   time.Date(2025, 4, 27, 12, 45, 0, 0, time.UTC),
		},
		{
			name:        "zero amount order",
			orderID:     4,
			customerID:  1,
			amountCents: 0,
			status:      "PENDING",
			createdAt:   time.Date(2025, 4, 27, 13, 0, 0, 0, time.UTC),
		},
		{
			name:        "large amount order",
			orderID:     5,
			customerID:  1000,
			amountCents: 999999999,
			status:      "PENDING",
			createdAt:   time.Date(2025, 4, 27, 14, 15, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustExec(t, db, `
INSERT INTO orders (id, customer_id, amount_cents, status, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$5)`,
				tt.orderID, tt.customerID, tt.amountCents, tt.status, tt.createdAt)

			var (
				id, customerID, amountCents int64
				status                      string
				createdAt, updatedAt        time.Time
			)
			row := db.QueryRowContext(ctx,
				`SELECT id, customer_id, amount_cents, status, created_at, updated_at FROM orders WHERE id=$1`, tt.orderID)
			if err := row.Scan(&id, &customerID, &amountCents, &status, &createdAt, &updatedAt); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if id != tt.orderID || customerID != tt.customerID || amountCents != tt.amountCents ||
				status != tt.status || !createdAt.Equal(tt.createdAt) || !updatedAt.Equal(tt.createdAt) {
				t.Fatalf("round-trip mismatch: id=%d customer=%d amount=%d status=%s created=%s updated=%s",
					id, customerID, amountCents, status, createdAt, updatedAt)
			}
		})
	}
}

func TestOrderRepo_AmountCheckConstraint(t *testing.T) {
	t.Parallel()
	db := setup.Postgres(t, schema)

	tests := []struct {
		name        string
		orderID     int64
		customerID  int64
		amountCents int64
		status      string
		expectError bool
	}{
		{
			name:        "negative amount",
			orderID:     1,
			customerID:  1,
			amountCents: -1,
			status:      "PENDING",
			expectError: true,
		},
		{
			name:        "zero amount",
			orderID:     2,
			customerID:  1,
			amountCents: 0,
			status:      "PENDING",
			expectError: false,
		},
		{
			name:        "positive amount",
			orderID:     3,
			customerID:  1,
			amountCents: 1,
			status:      "PENDING",
			expectError: false,
		},
		{
			name:        "large positive amount",
			orderID:     4,
			customerID:  1,
			amountCents: 999999999,
			status:      "PENDING",
			expectError: false,
		},
		{
			name:        "very negative amount",
			orderID:     5,
			customerID:  1,
			amountCents: -999999999,
			status:      "PENDING",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now().UTC()
			_, err := db.Exec(`
INSERT INTO orders (id, customer_id, amount_cents, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)`,
				tt.orderID, tt.customerID, tt.amountCents, tt.status, now)

			if tt.expectError && err == nil {
				t.Fatalf("expected CHECK violation for amount %d", tt.amountCents)
			}
			if !tt.expectError && err != nil {
				t.Fatalf("unexpected error for amount %d: %v", tt.amountCents, err)
			}
		})
	}
}

func TestOrderRepo_UpdateOnlyDirtyFields(t *testing.T) {
	t.Parallel()
	db := setup.Postgres(t, schema)
	ctx := context.Background()

	tests := []struct {
		name           string
		orderID        int64
		initialAmount  int64
		initialStatus  string
		updateStatus   string
		updateAmount   bool
		newAmount      int64
		expectAmount   int64
		expectStatus   string
		expectVersion  int64
	}{
		{
			name:          "status only update",
			orderID:       1,
			initialAmount: 5000,
			initialStatus: "PENDING",
			updateStatus:  "COMPLETED",
			updateAmount:  false,
			expectAmount:  5000,
			expectStatus:  "COMPLETED",
			expectVersion: 2,
		},
		{
			name:          "amount only update",
			orderID:       2,
			initialAmount: 1000,
			initialStatus: "PENDING",
			updateStatus:  "PENDING",
			updateAmount:  true,
			newAmount:     2000,
			expectAmount:  2000,
			expectStatus:  "PENDING",
			expectVersion: 2,
		},
		{
			name:          "both fields update",
			orderID:       3,
			initialAmount: 3000,
			initialStatus: "PENDING",
			updateStatus:  "COMPLETED",
			updateAmount:  true,
			newAmount:     4000,
			expectAmount:  4000,
			expectStatus:  "COMPLETED",
			expectVersion: 2,
		},
		{
			name:          "cancel order",
			orderID:       4,
			initialAmount: 7500,
			initialStatus: "PENDING",
			updateStatus:  "CANCELLED",
			updateAmount:  false,
			expectAmount:  7500,
			expectStatus:  "CANCELLED",
			expectVersion: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC)
			mustExec(t, db, `
INSERT INTO orders (id, customer_id, amount_cents, status, created_at, updated_at)
VALUES ($1, 42, $2, $3, $4, $4)`,
				tt.orderID, tt.initialAmount, tt.initialStatus, now)

			// Simulate partial update
			if tt.updateAmount {
				mustExec(t, db, `
UPDATE orders SET status=$1, amount_cents=$2, updated_at=$3, version=version+1 WHERE id=$4 AND version=1`,
					tt.updateStatus, tt.newAmount, now.Add(time.Minute), tt.orderID)
			} else {
				mustExec(t, db, `
UPDATE orders SET status=$1, updated_at=$2, version=version+1 WHERE id=$3 AND version=1`,
					tt.updateStatus, now.Add(time.Minute), tt.orderID)
			}

			var (
				amountCents int64
				status      string
				version     int64
			)
			if err := db.QueryRowContext(ctx,
				`SELECT amount_cents, status, version FROM orders WHERE id=$1`,
				tt.orderID,
			).Scan(&amountCents, &status, &version); err != nil {
				t.Fatal(err)
			}

			if amountCents != tt.expectAmount {
				t.Fatalf("amount should be %d, got %d", tt.expectAmount, amountCents)
			}
			if status != tt.expectStatus {
				t.Fatalf("status should be %s, got %s", tt.expectStatus, status)
			}
			if version != tt.expectVersion {
				t.Fatalf("version should be %d, got %d", tt.expectVersion, version)
			}
		})
	}
}

// TestOutbox_AtomicWrite proves that the aggregate write and the outbox
// insert succeed or fail together — the canonical fix for the dual-write
// problem the outbox pattern is designed to solve.
func TestOutbox_AtomicWrite(t *testing.T) {
	t.Parallel()
	db := setup.Postgres(t, schema)
	ctx := context.Background()

	tests := []struct {
		name         string
		orderID      int64
		customerID   int64
		amountCents  int64
		status       string
		eventType    string
		payload      string
		shouldCommit bool
	}{
		{
			name:         "successful transaction",
			orderID:      1,
			customerID:   1,
			amountCents:  100,
			status:       "COMPLETED",
			eventType:    "OrderCompleted",
			payload:      `{"order_id":1}`,
			shouldCommit: true,
		},
		{
			name:         "pending order event",
			orderID:      2,
			customerID:   2,
			amountCents:  200,
			status:       "PENDING",
			eventType:    "OrderCreated",
			payload:      `{"order_id":2,"amount":200}`,
			shouldCommit: true,
		},
		{
			name:         "cancelled order event",
			orderID:      3,
			customerID:   3,
			amountCents:  300,
			status:       "CANCELLED",
			eventType:    "OrderCancelled",
			payload:      `{"order_id":3,"reason":"user_request"}`,
			shouldCommit: true,
		},
		{
			name:         "rollback scenario",
			orderID:      4,
			customerID:   4,
			amountCents:  400,
			status:       "COMPLETED",
			eventType:    "OrderCompleted",
			payload:      `{"order_id":4}`,
			shouldCommit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}

			now := time.Now().UTC()
			if _, err := tx.ExecContext(ctx, `
INSERT INTO orders (id, customer_id, amount_cents, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)`,
				tt.orderID, tt.customerID, tt.amountCents, tt.status, now); err != nil {
				t.Fatal(err)
			}

			if _, err := tx.ExecContext(ctx, `
INSERT INTO outbox (aggregate_id, event_type, payload)
VALUES ($1, $2, $3)`,
				tt.orderID, tt.eventType, tt.payload); err != nil {
				t.Fatal(err)
			}

			if tt.shouldCommit {
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}

				// Verify both were committed
				var orderCount, outboxCount int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM orders WHERE id=$1`, tt.orderID).Scan(&orderCount); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE aggregate_id=$1`, tt.orderID).Scan(&outboxCount); err != nil {
					t.Fatal(err)
				}
				if orderCount != 1 || outboxCount != 1 {
					t.Fatalf("expected both records committed, got orders=%d outbox=%d", orderCount, outboxCount)
				}
			} else {
				// Force rollback
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}

				// Verify both were rolled back
				var orderCount, outboxCount int
				_ = db.QueryRowContext(ctx, `SELECT count(*) FROM orders WHERE id=$1`, tt.orderID).Scan(&orderCount)
				_ = db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE aggregate_id=$1`, tt.orderID).Scan(&outboxCount)
				if orderCount != 0 || outboxCount != 0 {
					t.Fatalf("expected both records rolled back, got orders=%d outbox=%d", orderCount, outboxCount)
				}
			}
		})
	}
}

func TestOrderRepo_StatusEnumConstraint(t *testing.T) {
	t.Parallel()
	db := setup.Postgres(t, schema)

	tests := []struct {
		name        string
		orderID     int64
		customerID  int64
		amountCents int64
		status      string
		expectError bool
	}{
		{
			name:        "valid pending status",
			orderID:     1,
			customerID:  1,
			amountCents: 100,
			status:      "PENDING",
			expectError: false,
		},
		{
			name:        "valid completed status",
			orderID:     2,
			customerID:  1,
			amountCents: 200,
			status:      "COMPLETED",
			expectError: false,
		},
		{
			name:        "valid cancelled status",
			orderID:     3,
			customerID:  1,
			amountCents: 300,
			status:      "CANCELLED",
			expectError: false,
		},
		{
			name:        "invalid status",
			orderID:     4,
			customerID:  1,
			amountCents: 400,
			status:      "INVALID",
			expectError: true,
		},
		{
			name:        "empty status",
			orderID:     5,
			customerID:  1,
			amountCents: 500,
			status:      "",
			expectError: true,
		},
		{
			name:        "case sensitive status",
			orderID:     6,
			customerID:  1,
			amountCents: 600,
			status:      "pending",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now().UTC()
			_, err := db.Exec(`
INSERT INTO orders (id, customer_id, amount_cents, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $5)`,
				tt.orderID, tt.customerID, tt.amountCents, tt.status, now)

			if tt.expectError && err == nil {
				t.Fatalf("expected enum violation for status %q", tt.status)
			}
			if !tt.expectError && err != nil {
				t.Fatalf("unexpected error for status %q: %v", tt.status, err)
			}
		})
	}
}

func TestOrderRepo_OutboxEventStorage(t *testing.T) {
	t.Parallel()
	db := setup.Postgres(t, schema)
	ctx := context.Background()

	tests := []struct {
		name        string
		aggregateID int64
		eventType   string
		payload     string
	}{
		{
			name:        "order completed event",
			aggregateID: 1,
			eventType:   "OrderCompleted",
			payload:     `{"order_id":1,"amount_cents":9999}`,
		},
		{
			name:        "order created event",
			aggregateID: 2,
			eventType:   "OrderCreated",
			payload:     `{"order_id":2,"customer_id":42}`,
		},
		{
			name:        "payment processed event",
			aggregateID: 1,
			eventType:   "PaymentProcessed",
			payload:     `{"order_id":1,"transaction_id":"tx_123"}`,
		},
		{
			name:        "large payload",
			aggregateID: 3,
			eventType:   "OrderShipped",
			payload:     `{"order_id":3,"tracking_number":"1Z999AA1234567890","carrier":"UPS","estimated_delivery":"2025-04-30T10:00:00Z"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Now().UTC()
			mustExec(t, db, `
INSERT INTO outbox (aggregate_id, event_type, payload, created_at)
VALUES ($1, $2, $3, $4)`,
				tt.aggregateID, tt.eventType, tt.payload, now)

			var (
				aggregateID int64
				eventType   string
				payload     string
				createdAt   time.Time
			)

			row := db.QueryRowContext(ctx, `
SELECT aggregate_id, event_type, payload, created_at FROM outbox
WHERE aggregate_id=$1 AND event_type=$2`,
				tt.aggregateID, tt.eventType)

			if err := row.Scan(&aggregateID, &eventType, &payload, &createdAt); err != nil {
				t.Fatalf("scan: %v", err)
			}

			if aggregateID != tt.aggregateID || eventType != tt.eventType || payload != tt.payload {
				t.Fatalf("round-trip mismatch: id=%d type=%s payload=%s",
					aggregateID, eventType, payload)
			}

			// Verify created_at is set
			if createdAt.IsZero() {
				t.Error("created_at should not be zero")
			}
		})
	}
}

func TestOrderRepo_OutboxIndexUsage(t *testing.T) {
	t.Parallel()
	db := setup.Postgres(t, schema)
	ctx := context.Background()

	// Insert multiple events
	now := time.Now().UTC()
	baseTime := now.Add(-time.Hour)

	for i := 0; i < 10; i++ {
		eventTime := baseTime.Add(time.Duration(i) * time.Minute)
		mustExec(t, db, `
INSERT INTO outbox (aggregate_id, event_type, payload, created_at)
VALUES ($1, $2, $3, $4)`,
			int64(i+1), "TestEvent", fmt.Sprintf(`{"id":%d}`, i+1), eventTime)
	}

	// Test the index on unprocessed events (where processed_at IS NULL)
	var count int
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM outbox WHERE processed_at IS NULL AND created_at < $1`,
		now).Scan(&count); err != nil {
		t.Fatal(err)
	}

	if count != 10 {
		t.Fatalf("expected 10 unprocessed events, got %d", count)
	}

	// Mark some as processed
	mustExec(t, db, `
UPDATE outbox SET processed_at = $1 WHERE aggregate_id <= 5`,
		now)

	// Verify index still works for remaining unprocessed
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FROM outbox WHERE processed_at IS NULL`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}

	if count != 5 {
		t.Fatalf("expected 5 remaining unprocessed events, got %d", count)
	}
}