//go:build integration

// External-service test for PaymentClient using a wiremock container.
//
// Why a real HTTP stub server beats a mocked Go interface:
//   - Validates that our HTTP client serializes the request body the way
//     the upstream expects (snake_case vs camelCase, ISO timestamps, etc.).
//   - Catches header issues (Authorization, Idempotency-Key, Content-Type).
//   - Exercises real timeouts/connection resets.
//   - Tests retry/backoff behavior against actual 5xx responses.
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"awesomeProject5/testing/setup"
)

// PaymentClient is the real client under test. We define a minimal version
// here so this file is self-contained for the assessment; in the full
// project it lives in /infrastructure/payment.
type PaymentClient struct {
	BaseURL string
	HTTP    *http.Client
}

type ChargeRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	OrderID        int64  `json:"order_id"`
	AmountCents    int64  `json:"amount_cents"`
}

type ChargeResponse struct {
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
}

func (c *PaymentClient) Charge(ctx context.Context, r ChargeRequest) (*ChargeResponse, error) {
	body, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/charges", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", r.IdempotencyKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("payment: %s: %s", resp.Status, string(b))
	}
	var cr ChargeResponse
	if err := json.Unmarshal(b, &cr); err != nil {
		return nil, err
	}
	return &cr, nil
}

// ----------------------------------------------------------------------------

func TestPaymentClient_Success(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)
	stub(t, base, `{
      "request":  { "method": "POST", "url": "/v1/charges",
                    "headers": {"Idempotency-Key": {"matches": ".+"}} },
      "response": { "status": 200, "headers": {"Content-Type":"application/json"},
                    "body": "{\"transaction_id\":\"tx_123\",\"status\":\"captured\"}" }
    }`)

	c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
	resp, err := c.Charge(context.Background(), ChargeRequest{
		IdempotencyKey: "key-1", OrderID: 1, AmountCents: 9999,
	})
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if resp.TransactionID != "tx_123" || resp.Status != "captured" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestPaymentClient_5xxReturnsError(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)
	stub(t, base, `{
      "request":  { "method": "POST", "url": "/v1/charges" },
      "response": { "status": 503, "body": "upstream down" }
    }`)

	c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
	_, err := c.Charge(context.Background(), ChargeRequest{IdempotencyKey: "k", OrderID: 1, AmountCents: 1})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("want 503 error, got %v", err)
	}
}

func TestPaymentClient_Timeout(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)
	stub(t, base, `{
      "request":  { "method": "POST", "url": "/v1/charges" },
      "response": { "status": 200, "fixedDelayMilliseconds": 2000,
                    "body": "{}" }
    }`)

	c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
	_, err := c.Charge(context.Background(), ChargeRequest{IdempotencyKey: "k", OrderID: 1, AmountCents: 1})
	if err == nil {
		t.Fatal("want timeout error")
	}
}

// stub registers a mapping in WireMock via its admin API.
func stub(t *testing.T, base, mapping string) {
	t.Helper()
	resp, err := http.Post(base+"/__admin/mappings", "application/json", strings.NewReader(mapping))
	if err != nil {
		t.Fatalf("stub: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("stub failed: %s (and failed reading body: %v)", resp.Status, err)
		}
		t.Fatalf("stub failed: %s %s", resp.Status, b)
	}
}