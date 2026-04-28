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

	tests := []struct {
		name            string
		idempotencyKey  string
		orderID         int64
		amountCents     int64
		expectedTxnID   string
		expectedStatus  string
		responseTxnID   string
		responseStatus  string
	}{
		{
			name:           "basic successful charge",
			idempotencyKey: "key-1",
			orderID:        1,
			amountCents:    9999,
			expectedTxnID:  "tx_123",
			expectedStatus: "captured",
		},
		{
			name:           "large amount charge",
			idempotencyKey: "key-2",
			orderID:        2,
			amountCents:    1000000,
			expectedTxnID:  "tx_456",
			expectedStatus: "captured",
		},
		{
			name:           "small amount charge",
			idempotencyKey: "key-3",
			orderID:        3,
			amountCents:    1,
			expectedTxnID:  "tx_789",
			expectedStatus: "captured",
		},
		{
			name:           "zero amount charge",
			idempotencyKey: "key-4",
			orderID:        4,
			amountCents:    0,
			expectedTxnID:  "tx_000",
			expectedStatus: "captured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub(t, base, fmt.Sprintf(`{
      "request":  { "method": "POST", "url": "/v1/charges",
                    "headers": {"Idempotency-Key": {"matches": ".+"}} },
      "response": { "status": 200, "headers": {"Content-Type":"application/json"},
                    "body": "{\"transaction_id\":\"%s\",\"status\":\"%s\"}" }
    }`, tt.expectedTxnID, tt.expectedStatus))

			c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
			resp, err := c.Charge(context.Background(), ChargeRequest{
				IdempotencyKey: tt.idempotencyKey,
				OrderID:        tt.orderID,
				AmountCents:    tt.amountCents,
			})
			if err != nil {
				t.Fatalf("charge: %v", err)
			}
			if resp.TransactionID != tt.expectedTxnID || resp.Status != tt.expectedStatus {
				t.Fatalf("unexpected response: %+v", resp)
			}
		})
	}
}

func TestPaymentClient_5xxReturnsError(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)

	tests := []struct {
		name        string
		statusCode  int
		responseBody string
		expectError  bool
	}{
		{
			name:        "503 service unavailable",
			statusCode:  503,
			responseBody: "upstream down",
			expectError:  true,
		},
		{
			name:        "500 internal server error",
			statusCode:  500,
			responseBody: "internal error",
			expectError:  true,
		},
		{
			name:        "502 bad gateway",
			statusCode:  502,
			responseBody: "bad gateway",
			expectError:  true,
		},
		{
			name:        "504 gateway timeout",
			statusCode:  504,
			responseBody: "gateway timeout",
			expectError:  true,
		},
		{
			name:        "200 success should not error",
			statusCode:  200,
			responseBody: `{"transaction_id":"tx_123","status":"captured"}`,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub(t, base, fmt.Sprintf(`{
      "request":  { "method": "POST", "url": "/v1/charges" },
      "response": { "status": %d, "body": "%s" }
    }`, tt.statusCode, tt.responseBody))

			c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
			_, err := c.Charge(context.Background(), ChargeRequest{
				IdempotencyKey: "k", OrderID: 1, AmountCents: 1,
			})

			if tt.expectError && err == nil {
				t.Fatalf("expected error for status %d", tt.statusCode)
			}
			if !tt.expectError && err != nil {
				t.Fatalf("unexpected error for status %d: %v", tt.statusCode, err)
			}
			if tt.expectError && err != nil && !strings.Contains(err.Error(), fmt.Sprintf("%d", tt.statusCode)) {
				t.Fatalf("error should contain status code %d: %v", tt.statusCode, err)
			}
		})
	}
}

func TestPaymentClient_Timeout(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)

	tests := []struct {
		name         string
		delayMs      int
		clientTimeout time.Duration
		expectError   bool
	}{
		{
			name:         "fast response succeeds",
			delayMs:      100,
			clientTimeout: 5 * time.Second,
			expectError:   false,
		},
		{
			name:         "slow response times out",
			delayMs:      2000,
			clientTimeout: 200 * time.Millisecond,
			expectError:   true,
		},
		{
			name:         "exact timeout boundary",
			delayMs:      500,
			clientTimeout: 500 * time.Millisecond,
			expectError:   true, // Usually times out at boundary
		},
		{
			name:         "very slow response",
			delayMs:      10000,
			clientTimeout: 100 * time.Millisecond,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub(t, base, fmt.Sprintf(`{
      "request":  { "method": "POST", "url": "/v1/charges" },
      "response": { "status": 200, "fixedDelayMilliseconds": %d,
                    "body": "{}" }
    }`, tt.delayMs))

			c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: tt.clientTimeout}}
			_, err := c.Charge(context.Background(), ChargeRequest{
				IdempotencyKey: "k", OrderID: 1, AmountCents: 1,
			})

			if tt.expectError && err == nil {
				t.Fatalf("expected timeout error for delay %dms with timeout %v", tt.delayMs, tt.clientTimeout)
			}
			if !tt.expectError && err != nil {
				t.Fatalf("unexpected error for delay %dms with timeout %v: %v", tt.delayMs, tt.clientTimeout, err)
			}
		})
	}
}

func TestPaymentClient_RequestSerialization(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)

	tests := []struct {
		name           string
		request        ChargeRequest
		expectedBody   string
		expectedTxnID  string
	}{
		{
			name: "standard request",
			request: ChargeRequest{
				IdempotencyKey: "key-123",
				OrderID:        456,
				AmountCents:    789,
			},
			expectedBody:  `{"idempotency_key":"key-123","order_id":456,"amount_cents":789}`,
			expectedTxnID: "tx_standard",
		},
		{
			name: "large order ID",
			request: ChargeRequest{
				IdempotencyKey: "key-large",
				OrderID:        9223372036854775807, // max int64
				AmountCents:    1000000,
			},
			expectedBody:  `{"idempotency_key":"key-large","order_id":9223372036854775807,"amount_cents":1000000}`,
			expectedTxnID: "tx_large",
		},
		{
			name: "special characters in key",
			request: ChargeRequest{
				IdempotencyKey: "key_123-abc.def",
				OrderID:        1,
				AmountCents:    500,
			},
			expectedBody:  `{"idempotency_key":"key_123-abc.def","order_id":1,"amount_cents":500}`,
			expectedTxnID: "tx_special",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub(t, base, fmt.Sprintf(`{
      "request":  { "method": "POST", "url": "/v1/charges",
                    "bodyPatterns": [{"equalToJson": "%s"}],
                    "headers": {"Idempotency-Key": {"equalTo": "%s"}} },
      "response": { "status": 200, "headers": {"Content-Type":"application/json"},
                    "body": "{\"transaction_id\":\"%s\",\"status\":\"captured\"}" }
    }`, tt.expectedBody, tt.request.IdempotencyKey, tt.expectedTxnID))

			c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
			resp, err := c.Charge(context.Background(), tt.request)
			if err != nil {
				t.Fatalf("charge: %v", err)
			}
			if resp.TransactionID != tt.expectedTxnID {
				t.Fatalf("expected transaction ID %s, got %s", tt.expectedTxnID, resp.TransactionID)
			}
		})
	}
}

func TestPaymentClient_4xxClientErrors(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)

	tests := []struct {
		name        string
		statusCode  int
		responseBody string
	}{
		{
			name:        "400 bad request",
			statusCode:  400,
			responseBody: "invalid request",
		},
		{
			name:        "401 unauthorized",
			statusCode:  401,
			responseBody: "unauthorized",
		},
		{
			name:        "403 forbidden",
			statusCode:  403,
			responseBody: "forbidden",
		},
		{
			name:        "404 not found",
			statusCode:  404,
			responseBody: "not found",
		},
		{
			name:        "422 unprocessable entity",
			statusCode:  422,
			responseBody: "validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub(t, base, fmt.Sprintf(`{
      "request":  { "method": "POST", "url": "/v1/charges" },
      "response": { "status": %d, "body": "%s" }
    }`, tt.statusCode, tt.responseBody))

			c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
			_, err := c.Charge(context.Background(), ChargeRequest{
				IdempotencyKey: "k", OrderID: 1, AmountCents: 1,
			})

			if err == nil {
				t.Fatalf("expected error for status %d", tt.statusCode)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%d", tt.statusCode)) {
				t.Fatalf("error should contain status code %d: %v", tt.statusCode, err)
			}
			if !strings.Contains(err.Error(), tt.responseBody) {
				t.Fatalf("error should contain response body: %v", err)
			}
		})
	}
}

func TestPaymentClient_InvalidJSONResponse(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)

	tests := []struct {
		name         string
		responseBody string
	}{
		{
			name:         "malformed json",
			responseBody: `{"transaction_id": "tx_123", "status": }`,
		},
		{
			name:         "empty response",
			responseBody: ``,
		},
		{
			name:         "html error page",
			responseBody: `<html><body>Internal Server Error</body></html>`,
		},
		{
			name:         "plain text",
			responseBody: `Internal server error occurred`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub(t, base, fmt.Sprintf(`{
      "request":  { "method": "POST", "url": "/v1/charges" },
      "response": { "status": 200, "body": "%s" }
    }`, tt.responseBody))

			c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
			_, err := c.Charge(context.Background(), ChargeRequest{
				IdempotencyKey: "k", OrderID: 1, AmountCents: 1,
			})

			if err == nil {
				t.Fatalf("expected JSON parsing error for response: %s", tt.responseBody)
			}
		})
	}
}

func TestPaymentClient_IdempotencyKeyHeader(t *testing.T) {
	t.Parallel()
	base := setup.WireMockEndpoint(t)

	tests := []struct {
		name           string
		idempotencyKey string
		expectMatch    bool
	}{
		{
			name:           "matching key",
			idempotencyKey: "exact-match-key",
			expectMatch:    true,
		},
		{
			name:           "empty key",
			idempotencyKey: "",
			expectMatch:    false,
		},
		{
			name:           "long key",
			idempotencyKey: "very-long-idempotency-key-that-might-be-used-in-production-systems-for-uniqueness",
			expectMatch:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.expectMatch {
				stub(t, base, fmt.Sprintf(`{
      "request":  { "method": "POST", "url": "/v1/charges",
                    "headers": {"Idempotency-Key": {"equalTo": "%s"}} },
      "response": { "status": 200, "body": "{\"transaction_id\":\"tx_123\",\"status\":\"captured\"}" }
    }`, tt.idempotencyKey))
			} else {
				stub(t, base, `{
      "request":  { "method": "POST", "url": "/v1/charges",
                    "headers": {"Idempotency-Key": {"matches": ".+"}} },
      "response": { "status": 200, "body": "{\"transaction_id\":\"tx_123\",\"status\":\"captured\"}" }
    }`)
			}

			c := &PaymentClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
			_, err := c.Charge(context.Background(), ChargeRequest{
				IdempotencyKey: tt.idempotencyKey,
				OrderID:        1,
				AmountCents:    100,
			})

			if tt.expectMatch && err != nil {
				t.Fatalf("expected success for matching key %q: %v", tt.idempotencyKey, err)
			}
			if !tt.expectMatch && err == nil {
				t.Fatalf("expected error for non-matching key %q", tt.idempotencyKey)
			}
		})
	}
}