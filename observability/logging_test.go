package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"test-1/observability"
)

// TestNopLogger tests that NopLogger operations don't error.
func TestNopLogger(t *testing.T) {
	t.Parallel()

	logger := observability.NopLogger{}
	ctx := context.Background()

	// These should not panic
	logger.Debug(ctx, "debug message")
	logger.Info(ctx, "info message")
	logger.Warn(ctx, "warn message")
	logger.Error(ctx, "error message", nil)
	logger.Error(ctx, "error message", context.Canceled)

	// With returns a new logger that should also not panic
	newLogger := logger.With(observability.String("key", "value"))
	newLogger.Info(ctx, "another message")
}

// TestSlogLogger_CreatesLogger tests that SlogLogger is created successfully.
func TestSlogLogger_CreatesLogger(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := observability.SlogConfig{
		Writer:  &buf,
		Level:   slog.LevelInfo,
		Service: "test-service",
		PIIKeys: []string{"email", "password"},
	}

	logger := observability.NewSlogLogger(cfg)
	if logger == nil {
		t.Fatal("Failed to create SlogLogger")
	}
}

// TestSlogLogger_LogLevels tests different log levels.
func TestSlogLogger_LogLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		level    slog.Level
		logFunc  func(ctx context.Context, logger observability.Logger, msg string)
		wantLogs int
	}{
		{
			name:    "Debug level",
			level:   slog.LevelDebug,
			logFunc: func(ctx context.Context, logger observability.Logger, msg string) { logger.Debug(ctx, msg) },
		},
		{
			name:    "Info level",
			level:   slog.LevelInfo,
			logFunc: func(ctx context.Context, logger observability.Logger, msg string) { logger.Info(ctx, msg) },
		},
		{
			name:    "Warn level",
			level:   slog.LevelWarn,
			logFunc: func(ctx context.Context, logger observability.Logger, msg string) { logger.Warn(ctx, msg) },
		},
		{
			name:    "Error level",
			level:   slog.LevelError,
			logFunc: func(ctx context.Context, logger observability.Logger, msg string) { logger.Error(ctx, msg, nil) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cfg := observability.SlogConfig{
				Writer: &buf,
				Level:  tt.level,
			}

			logger := observability.NewSlogLogger(cfg)
			ctx := context.Background()
			tt.logFunc(ctx, logger, "test message")

			// Verify something was logged
			if buf.Len() == 0 {
				t.Error("Expected log output")
			}
		})
	}
}

// TestSlogLogger_Attributes tests that attributes are logged.
func TestSlogLogger_Attributes(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := observability.SlogConfig{
		Writer: &buf,
		Level:  slog.LevelInfo,
	}

	logger := observability.NewSlogLogger(cfg)
	ctx := context.Background()

	logger.Info(ctx, "test", observability.String("key1", "value1"), observability.Int64("key2", 42))

	// Parse the JSON output
	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Failed to parse log JSON: %v", err)
	}

	if val, ok := entry["key1"]; !ok || val != "value1" {
		t.Error("String attribute not found or incorrect")
	}
	if val, ok := entry["key2"]; !ok || val != float64(42) {
		t.Error("Int64 attribute not found or incorrect")
	}
}

// TestSlogLogger_PIIRedaction tests PII key redaction.
func TestSlogLogger_PIIRedaction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		piiKeys []string
		attrs   []observability.Attr
		check   func(entry map[string]interface{}) bool
	}{
		{
			name:    "redact email",
			piiKeys: []string{"email"},
			attrs: []observability.Attr{
				observability.String("email", "user@example.com"),
				observability.String("name", "John"),
			},
			check: func(entry map[string]interface{}) bool {
				email, ok := entry["email"].(string)
				return ok && email == "[REDACTED]"
			},
		},
		{
			name:    "redact multiple keys",
			piiKeys: []string{"email", "password", "phone"},
			attrs: []observability.Attr{
				observability.String("email", "user@example.com"),
				observability.String("password", "secret123"),
				observability.String("phone", "555-1234"),
				observability.String("name", "John"),
			},
			check: func(entry map[string]interface{}) bool {
				email, _ := entry["email"].(string)
				password, _ := entry["password"].(string)
				phone, _ := entry["phone"].(string)
				return email == "[REDACTED]" && password == "[REDACTED]" && phone == "[REDACTED]"
			},
		},
		{
			name:    "case insensitive redaction",
			piiKeys: []string{"email"},
			attrs: []observability.Attr{
				observability.String("EMAIL", "user@example.com"),
			},
			check: func(entry map[string]interface{}) bool {
				email, ok := entry["EMAIL"].(string)
				return ok && email == "[REDACTED]"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cfg := observability.SlogConfig{
				Writer:  &buf,
				Level:   slog.LevelInfo,
				PIIKeys: tt.piiKeys,
			}

			logger := observability.NewSlogLogger(cfg)
			ctx := context.Background()

			logger.Info(ctx, "test", tt.attrs...)

			var entry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("Failed to parse log JSON: %v", err)
			}

			if !tt.check(entry) {
				t.Error("PII redaction check failed")
			}
		})
	}
}

// TestSlogLogger_ServiceAttribute tests that service attribute is added.
func TestSlogLogger_ServiceAttribute(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := observability.SlogConfig{
		Writer:  &buf,
		Level:   slog.LevelInfo,
		Service: "my-service",
	}

	logger := observability.NewSlogLogger(cfg)
	ctx := context.Background()

	logger.Info(ctx, "test")

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Failed to parse log JSON: %v", err)
	}

	if service, ok := entry["service"].(string); !ok || service != "my-service" {
		t.Error("Service attribute not found or incorrect")
	}
}

// TestSlogLogger_ErrorIncludesStack tests that Error logs include stack trace.
func TestSlogLogger_ErrorIncludesStack(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := observability.SlogConfig{
		Writer: &buf,
		Level:  slog.LevelInfo,
	}

	logger := observability.NewSlogLogger(cfg)
	ctx := context.Background()

	logger.Error(ctx, "error occurred", context.DeadlineExceeded)

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Failed to parse log JSON: %v", err)
	}

	// Check that error and stack fields exist
	if _, ok := entry["error"]; !ok {
		t.Error("Error field not found in error log")
	}
	if _, ok := entry["stack"]; !ok {
		t.Error("Stack field not found in error log")
	}
}

// TestSlogLogger_With tests the With method returns a new logger with attributes.
func TestSlogLogger_With(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := observability.SlogConfig{
		Writer: &buf,
		Level:  slog.LevelInfo,
	}

	logger := observability.NewSlogLogger(cfg)
	ctx := context.Background()

	// Create a new logger with context attributes
	contextLogger := logger.With(
		observability.String("request_id", "req-123"),
		observability.String("user_id", "user-456"),
	)

	contextLogger.Info(ctx, "user action")

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Failed to parse log JSON: %v", err)
	}

	if reqID, ok := entry["request_id"].(string); !ok || reqID != "req-123" {
		t.Error("request_id not found in logger.With")
	}
	if userID, ok := entry["user_id"].(string); !ok || userID != "user-456" {
		t.Error("user_id not found in logger.With")
	}
}

// TestSlogLogger_JSONStructured tests that output is valid JSON.
func TestSlogLogger_JSONStructured(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := observability.SlogConfig{
		Writer: &buf,
		Level:  slog.LevelInfo,
	}

	logger := observability.NewSlogLogger(cfg)
	ctx := context.Background()

	logger.Info(ctx, "structured log", observability.String("key", "value"))

	// Must be valid JSON
	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Output is not valid JSON: %v", err)
	}

	// Must have required fields
	if _, ok := entry["msg"]; !ok {
		t.Error("msg field missing from structured log")
	}
	if _, ok := entry["level"]; !ok {
		t.Error("level field missing from structured log")
	}
}

// TestSlogLogger_TraceCorrelation tests that trace ID is added from context.
func TestSlogLogger_TraceCorrelation(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	cfg := observability.SlogConfig{
		Writer: &buf,
		Level:  slog.LevelInfo,
	}

	logger := observability.NewSlogLogger(cfg)
	tracer := observability.NewInMemoryTracer()

	// Create a span
	ctx, span := tracer.Start(context.Background(), "test")
	defer span.End()

	logger.Info(ctx, "operation", observability.String("action", "create"))

	var entry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("Failed to parse log JSON: %v", err)
	}

	// Trace ID and span ID should be added from the context
	if traceID, ok := entry["trace_id"].(string); !ok || traceID == "" {
		t.Error("trace_id not found in log or empty")
	}
	if spanID, ok := entry["span_id"].(string); !ok || spanID == "" {
		t.Error("span_id not found in log or empty")
	}
}
