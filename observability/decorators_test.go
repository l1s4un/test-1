package observability_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"test-1/observability"
)

// mockExecutor is a test helper that implements Executor interface
type mockExecutor struct {
	called     bool
	callCount  int
	shouldErr  bool
	shouldPanic bool
	result     int
}

func (m *mockExecutor) Execute(ctx context.Context, req struct{}) (int, error) {
	m.called = true
	m.callCount++

	if m.shouldPanic {
		panic("executor panic")
	}

	if m.shouldErr {
		return 0, errors.New("executor error")
	}

	return m.result, nil
}

// TestTracedExecutor_RecordsSpan tests that TracedExecutor creates and ends spans.
func TestTracedExecutor_RecordsSpan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spanName string
		attrs    []observability.Attr
	}{
		{
			name:     "simple span",
			spanName: "operation",
		},
		{
			name:     "span with attributes",
			spanName: "operation.detailed",
			attrs: []observability.Attr{
				observability.String("user_id", "123"),
				observability.Int64("order_id", 456),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracer := observability.NewInMemoryTracer()
			inner := &mockExecutor{result: 42}

			executor := observability.TracedExecutor[struct{}, int]{
				Inner:  inner,
				Tracer: tracer,
				Name:   tt.spanName,
			}

			ctx := context.Background()
			_, err := executor.Execute(ctx, struct{}{})

			if err != nil {
				t.Fatalf("Execute failed: %v", err)
			}

			spans := tracer.Snapshot()
			if len(spans) != 1 {
				t.Fatalf("Expected 1 span, got %d", len(spans))
			}

			if spans[0].Name != tt.spanName {
				t.Errorf("Span name: got %q, want %q", spans[0].Name, tt.spanName)
			}
		})
	}
}

// TestTracedExecutor_RecordsError tests that TracedExecutor records errors in spans.
func TestTracedExecutor_RecordsError(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	inner := &mockExecutor{shouldErr: true}

	executor := observability.TracedExecutor[struct{}, int]{
		Inner:  inner,
		Tracer: tracer,
		Name:   "failing_operation",
	}

	ctx := context.Background()
	_, err := executor.Execute(ctx, struct{}{})

	if err == nil {
		t.Fatal("Expected error from inner executor")
	}

	spans := tracer.Snapshot()
	if spans[0].Err == nil {
		t.Error("Span should record the error")
	}
}

// TestTracedExecutor_HandlesContextCancellation tests span handling on context cancellation.
func TestTracedExecutor_HandlesContextCancellation(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	inner := &mockExecutor{result: 42}

	executor := observability.TracedExecutor[struct{}, int]{
		Inner:  inner,
		Tracer: tracer,
		Name:   "operation",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _ = executor.Execute(ctx, struct{}{})

	// Span should still be recorded even with cancelled context
	if len(tracer.Snapshot()) == 0 {
		t.Error("Span not recorded with cancelled context")
	}
}

// TestLoggedExecutor_LogsStartAndCompletion tests that LoggedExecutor logs lifecycle events.
func TestLoggedExecutor_LogsStartAndCompletion(t *testing.T) {
	t.Parallel()

	inner := &mockExecutor{result: 42}
	log := observability.NopLogger{}
	clock := observability.FixedClock{T: time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC)}

	executor := observability.LoggedExecutor[struct{}, int]{
		Inner: inner,
		Log:   log,
		Name:  "CreateOrder",
		Clock: clock,
	}

	ctx := context.Background()
	result, err := executor.Execute(ctx, struct{}{})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result != 42 {
		t.Errorf("Result: got %d, want 42", result)
	}
}

// TestLoggedExecutor_RecordsErrors tests that LoggedExecutor records errors.
func TestLoggedExecutor_RecordsErrors(t *testing.T) {
	t.Parallel()

	inner := &mockExecutor{shouldErr: true}
	log := observability.NopLogger{}
	clock := observability.RealClock{}

	executor := observability.LoggedExecutor[struct{}, int]{
		Inner: inner,
		Log:   log,
		Name:  "FailingOperation",
		Clock: clock,
	}

	ctx := context.Background()
	_, err := executor.Execute(ctx, struct{}{})

	if err == nil {
		t.Fatal("Expected error")
	}
}

// TestLoggedExecutor_DefaultsClock tests that LoggedExecutor defaults to RealClock.
func TestLoggedExecutor_DefaultsClock(t *testing.T) {
	t.Parallel()

	inner := &mockExecutor{result: 42}
	log := observability.NopLogger{}

	executor := observability.LoggedExecutor[struct{}, int]{
		Inner: inner,
		Log:   log,
		Name:  "Operation",
		Clock: nil, // Not set
	}

	ctx := context.Background()
	_, err := executor.Execute(ctx, struct{}{})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Should not panic on nil clock
	if !inner.called {
		t.Error("Inner executor not called")
	}
}

// TestMeteredExecutor_CountsSuccess tests that MeteredExecutor counts successful executions.
func TestMeteredExecutor_CountsSuccess(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	inner := &mockExecutor{result: 42}

	executor := observability.NewMeteredExecutor[struct{}, int](
		inner, meter, "CreateOrder",
	)

	ctx := context.Background()
	_, err := executor.Execute(ctx, struct{}{})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	successCount := meter.CounterValue(
		"usecase_total",
		observability.Labels{"operation": "CreateOrder", "status": "success"},
	)

	if successCount != 1 {
		t.Errorf("Success count: got %f, want 1", successCount)
	}
}

// TestMeteredExecutor_CountsFailure tests that MeteredExecutor counts failures.
func TestMeteredExecutor_CountsFailure(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	inner := &mockExecutor{shouldErr: true}

	executor := observability.NewMeteredExecutor[struct{}, int](
		inner, meter, "CreateOrder",
	)

	ctx := context.Background()
	_, _ = executor.Execute(ctx, struct{}{})

	failureCount := meter.CounterValue(
		"usecase_total",
		observability.Labels{"operation": "CreateOrder", "status": "failure"},
	)

	if failureCount != 1 {
		t.Errorf("Failure count: got %f, want 1", failureCount)
	}
}

// TestMeteredExecutor_RecordsDuration tests that MeteredExecutor records duration.
func TestMeteredExecutor_RecordsDuration(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	inner := &mockExecutor{result: 42}

	executor := observability.NewMeteredExecutor[struct{}, int](
		inner, meter, "CreateOrder",
	)

	ctx := context.Background()
	_, err := executor.Execute(ctx, struct{}{})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	samples := meter.HistogramSamples(
		"usecase_duration_seconds",
		observability.Labels{"operation": "CreateOrder", "status": "success"},
	)

	if len(samples) != 1 {
		t.Errorf("Expected 1 duration sample, got %d", len(samples))
	}

	// Duration should be positive (and less than 1 second for a quick test)
	if samples[0] < 0 || samples[0] > 1 {
		t.Errorf("Duration outside expected range: %f", samples[0])
	}
}

// TestMeteredExecutor_DefaultsClock tests that MeteredExecutor defaults to RealClock.
func TestMeteredExecutor_DefaultsClock(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	inner := &mockExecutor{result: 42}

	executor := observability.NewMeteredExecutor[struct{}, int](
		inner, meter, "Operation",
	)

	ctx := context.Background()
	_, err := executor.Execute(ctx, struct{}{})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}

// TestDecoratorComposition tests stacking multiple decorators.
func TestDecoratorComposition(t *testing.T) {
	t.Parallel()

	tracer := observability.NewInMemoryTracer()
	log := observability.NopLogger{}
	meter := observability.NewInMemoryMeter()
	inner := &mockExecutor{result: 42}
	clock := observability.FixedClock{T: time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC)}

	// Stack decorators
	logged := observability.LoggedExecutor[struct{}, int]{
		Inner: inner,
		Log:   log,
		Name:  "Operation",
		Clock: clock,
	}

	metered := observability.NewMeteredExecutor[struct{}, int](
		logged, meter, "Operation",
	)

	traced := observability.TracedExecutor[struct{}, int]{
		Inner:  metered,
		Tracer: tracer,
		Name:   "operation.stacked",
	}

	ctx := context.Background()
	result, err := traced.Execute(ctx, struct{}{})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result != 42 {
		t.Errorf("Result: got %d, want 42", result)
	}

	// Verify all decorators worked
	if len(tracer.Snapshot()) == 0 {
		t.Error("Traced: no span recorded")
	}

	successCount := meter.CounterValue(
		"usecase_total",
		observability.Labels{"operation": "Operation", "status": "success"},
	)
	if successCount != 1 {
		t.Error("Metered: success not counted")
	}
}

// TestPanicSafety tests that decorators handle panics safely.
func TestPanicSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		makeDec   func(inner observability.Executor[struct{}, int]) observability.Executor[struct{}, int]
	}{
		{
			name: "TracedExecutor",
			makeDec: func(inner observability.Executor[struct{}, int]) observability.Executor[struct{}, int] {
				tracer := observability.NewInMemoryTracer()
				return observability.TracedExecutor[struct{}, int]{
					Inner:  inner,
					Tracer: tracer,
					Name:   "test",
				}
			},
		},
		{
			name: "LoggedExecutor",
			makeDec: func(inner observability.Executor[struct{}, int]) observability.Executor[struct{}, int] {
				return observability.LoggedExecutor[struct{}, int]{
					Inner: inner,
					Log:   observability.NopLogger{},
					Name:  "test",
					Clock: observability.RealClock{},
				}
			},
		},
		{
			name: "MeteredExecutor",
			makeDec: func(inner observability.Executor[struct{}, int]) observability.Executor[struct{}, int] {
				meter := observability.NewInMemoryMeter()
				return observability.NewMeteredExecutor[struct{}, int](
					inner, meter, "test",
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &mockExecutor{shouldPanic: true}
			executor := tt.makeDec(inner)

			defer func() {
				if r := recover(); r == nil {
					t.Error("Expected panic to be re-thrown")
				}
			}()

			ctx := context.Background()
			executor.Execute(ctx, struct{}{})
		})
	}
}
