package observability_test

import (
	"context"
	"testing"

	"test-1/observability"
)

// TestNoopTracer tests the no-op tracer implementation.
func TestNoopTracer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "single start"},
		{name: "multiple starts"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := observability.NewNoopTracer()
			ctx := context.Background()

			newCtx, span := tracer.Start(ctx, "test.operation")

			// Noop span should not panic on any operation
			span.SetAttributes(observability.String("key", "value"))
			span.AddEvent("test_event", observability.String("k", "v"))
			span.RecordError(nil)
			span.End()

			// Noop span should return empty strings for IDs
			if span.TraceID() != "" {
				t.Errorf("Noop span TraceID should be empty, got %q", span.TraceID())
			}
			if span.SpanID() != "" {
				t.Errorf("Noop span SpanID should be empty, got %q", span.SpanID())
			}

			// Context should be unchanged
			if newCtx != ctx {
				t.Error("Noop tracer should not modify context")
			}
		})
	}
}

// TestInMemoryTracer_StartCreatesSpan tests that InMemoryTracer creates spans.
func TestInMemoryTracer_StartCreatesSpan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		spanName  string
		attrCount int
	}{
		{
			name:      "simple span",
			spanName:  "operation",
			attrCount: 0,
		},
		{
			name:      "span with attributes",
			spanName:  "operation.with.attrs",
			attrCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := observability.NewInMemoryTracer()
			ctx := context.Background()

			attrs := []observability.Attr{
				observability.String("user_id", "123"),
				observability.Int64("order_id", 456),
				observability.Bool("is_admin", true),
			}[:tt.attrCount]

			newCtx, span := tracer.Start(ctx, tt.spanName, attrs...)

			// Verify span is recorded
			if len(tracer.Snapshot()) != 1 {
				t.Errorf("Expected 1 span, got %d", len(tracer.Snapshot()))
			}

			rec := tracer.Snapshot()[0]
			if rec.Name != tt.spanName {
				t.Errorf("Span name: got %q, want %q", rec.Name, tt.spanName)
			}

			if len(rec.Attrs) != tt.attrCount {
				t.Errorf("Span attrs: got %d, want %d", len(rec.Attrs), tt.attrCount)
			}

			// Span should have non-empty IDs
			if rec.TraceID == "" {
				t.Error("TraceID should not be empty")
			}
			if rec.SpanID == "" {
				t.Error("SpanID should not be empty")
			}

			// Context should contain the span
			if observability.SpanFromContext(newCtx).TraceID() == "" {
				t.Error("Span not found in context")
			}

			span.End()
		})
	}
}

// TestInMemoryTracer_TraceIDConsistency tests that child spans inherit parent trace ID.
func TestInMemoryTracer_TraceIDConsistency(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	ctx := context.Background()

	// Create parent span
	parentCtx, parentSpan := tracer.Start(ctx, "parent")
	parentTraceID := parentSpan.TraceID()

	// Create child span in parent context
	childCtx, childSpan := tracer.Start(parentCtx, "child")
	childTraceID := childSpan.TraceID()

	// Both should have the same trace ID
	if parentTraceID != childTraceID {
		t.Errorf("Parent and child trace IDs differ: parent=%q, child=%q", parentTraceID, childTraceID)
	}

	// Child should have parent's span ID as parent ID
	childRec := tracer.Snapshot()[1]
	if childRec.ParentID != parentSpan.SpanID() {
		t.Errorf("Child parent ID: got %q, want %q", childRec.ParentID, parentSpan.SpanID())
	}

	parentSpan.End()
	childSpan.End()
}

// TestInMemoryTracer_SpanFromContext tests SpanFromContext.
func TestInMemoryTracer_SpanFromContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		hasCtx bool
	}{
		{name: "with span in context", hasCtx: true},
		{name: "without span in context", hasCtx: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ctx context.Context

			if tt.hasCtx {
				tracer := observability.NewInMemoryTracer()
				var span observability.Span
				ctx, span = tracer.Start(context.Background(), "test")
				defer span.End()
			} else {
				ctx = context.Background()
			}

			span := observability.SpanFromContext(ctx)

			if tt.hasCtx {
				if span.TraceID() == "" {
					t.Error("Expected to find span in context")
				}
			} else {
				if span.TraceID() != "" {
					t.Error("Expected noop span when none in context")
				}
			}
		})
	}
}

// TestInMemoryTracer_SpanRecordsEvents tests that AddEvent records events.
func TestInMemoryTracer_SpanRecordsEvents(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	ctx := context.Background()

	_, span := tracer.Start(ctx, "test")

	span.AddEvent("event1", observability.String("key", "val1"))
	span.AddEvent("event2", observability.Int64("id", 123))
	span.End()

	rec := tracer.Snapshot()[0]
	if len(rec.Events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(rec.Events))
	}

	if rec.Events[0].Name != "event1" {
		t.Errorf("Event 0 name: got %q, want event1", rec.Events[0].Name)
	}
	if rec.Events[1].Name != "event2" {
		t.Errorf("Event 1 name: got %q, want event2", rec.Events[1].Name)
	}
}

// TestInMemoryTracer_SpanRecordsError tests that RecordError records errors.
func TestInMemoryTracer_SpanRecordsError(t *testing.T) {
	t.Parallel()

	type testErr struct {
		err  error
		name string
	}

	tests := []testErr{
		{nil, "nil error"},
		{context.Canceled, "context canceled"},
		{context.DeadlineExceeded, "deadline exceeded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := observability.NewInMemoryTracer()
			ctx := context.Background()

			_, span := tracer.Start(ctx, "test")
			span.RecordError(tt.err)
			span.End()

			rec := tracer.Snapshot()[0]
			if tt.err == nil {
				if rec.Err != nil {
					t.Error("Expected nil error")
				}
			} else {
				if rec.Err != tt.err {
					t.Errorf("Error: got %v, want %v", rec.Err, tt.err)
				}
			}
		})
	}
}

// TestInMemoryTracer_SetAttributes tests that SetAttributes records attributes.
func TestInMemoryTracer_SetAttributes(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	ctx := context.Background()

	_, span := tracer.Start(ctx, "test", observability.String("initial", "value"))
	span.SetAttributes(
		observability.String("key1", "val1"),
		observability.Int64("key2", 42),
		observability.Bool("key3", true),
	)
	span.End()

	rec := tracer.Snapshot()[0]
	if len(rec.Attrs) != 4 { // initial + 3 from SetAttributes
		t.Errorf("Expected 4 attrs, got %d", len(rec.Attrs))
	}
}

// TestInMemoryTracer_MultipleSpans tests tracking multiple spans.
func TestInMemoryTracer_MultipleSpans(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	ctx := context.Background()

	// Create multiple spans
	for i := 0; i < 5; i++ {
		_, span := tracer.Start(ctx, "operation")
		span.End()
	}

	if len(tracer.Snapshot()) != 5 {
		t.Errorf("Expected 5 spans, got %d", len(tracer.Snapshot()))
	}
}

// TestInMemoryTracer_Snapshot tests that Snapshot is safe.
func TestInMemoryTracer_Snapshot(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	ctx := context.Background()

	_, span := tracer.Start(ctx, "test")
	snapshot1 := tracer.Snapshot()
	span.End()
	snapshot2 := tracer.Snapshot()

	// Snapshots should be copies
	if len(snapshot1) != 1 {
		t.Errorf("Snapshot before end: expected 1, got %d", len(snapshot1))
	}
	if len(snapshot2) != 1 {
		t.Errorf("Snapshot after end: expected 1, got %d", len(snapshot2))
	}

	// Modifying one shouldn't affect the other
	if &snapshot1[0] == &snapshot2[0] {
		t.Error("Snapshots should be independent copies")
	}
}

// TestInMemoryTracer_NestedSpans tests parent-child span relationships.
func TestInMemoryTracer_NestedSpans(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	ctx := context.Background()

	// Create a hierarchy
	parentCtx, parent := tracer.Start(ctx, "parent")
	childCtx, child := tracer.Start(parentCtx, "child")
	grandchildCtx, grandchild := tracer.Start(childCtx, "grandchild")

	grandchild.End()
	child.End()
	parent.End()

	spans := tracer.Snapshot()
	if len(spans) != 3 {
		t.Errorf("Expected 3 spans, got %d", len(spans))
	}

	// Verify hierarchy
	if spans[1].ParentID != spans[0].SpanID {
		t.Error("Child parent ID should match parent span ID")
	}
	if spans[2].ParentID != spans[1].SpanID {
		t.Error("Grandchild parent ID should match child span ID")
	}
}
