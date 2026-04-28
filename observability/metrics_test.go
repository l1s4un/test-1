package observability_test

import (
	"context"
	"math"
	"testing"

	"test-1/observability"
)

// TestInMemoryMeter_Counter tests counter operations.
func TestInMemoryMeter_Counter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		operations []struct {
			op    string // "Inc" or "Add"
			value float64
		}
		expected float64
		wantErr  bool
	}{
		{
			name: "single increment",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Inc", value: 1},
			},
			expected: 1,
		},
		{
			name: "multiple increments",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Inc", value: 1},
				{op: "Inc", value: 1},
				{op: "Inc", value: 1},
			},
			expected: 3,
		},
		{
			name: "add positive value",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Add", value: 5.5},
			},
			expected: 5.5,
		},
		{
			name: "add then increment",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Add", value: 10},
				{op: "Inc", value: 1},
			},
			expected: 11,
		},
		{
			name: "add zero",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Inc", value: 1},
				{op: "Add", value: 0},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meter := observability.NewInMemoryMeter()
			counter := meter.Counter("test_counter", "status")
			ctx := context.Background()
			labels := observability.Labels{"status": "success"}

			for _, op := range tt.operations {
				if op.op == "Inc" {
					counter.Inc(ctx, labels)
				} else {
					counter.Add(ctx, op.value, labels)
				}
			}

			result := meter.CounterValue("test_counter", labels)
			if math.Abs(result-tt.expected) > 0.0001 {
				t.Errorf("Counter value: got %f, want %f", result, tt.expected)
			}
		})
	}
}

// TestInMemoryMeter_CounterPanicOnNegative tests that counter panics on negative values.
func TestInMemoryMeter_CounterPanicOnNegative(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	counter := meter.Counter("test_counter")
	ctx := context.Background()
	labels := observability.Labels{}

	// Should panic on negative value
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic on negative Add, but none occurred")
		}
	}()

	counter.Add(ctx, -5, labels)
}

// TestInMemoryMeter_CounterPanicOnNaN tests that counter panics on NaN values.
func TestInMemoryMeter_CounterPanicOnNaN(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	counter := meter.Counter("test_counter")
	ctx := context.Background()
	labels := observability.Labels{}

	// Should panic on NaN
	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic on NaN Add, but none occurred")
		}
	}()

	counter.Add(ctx, math.NaN(), labels)
}

// TestInMemoryMeter_Histogram tests histogram operations.
func TestInMemoryMeter_Histogram(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		observations []float64
		expectedLen int
	}{
		{
			name:        "single observation",
			observations: []float64{0.5},
			expectedLen: 1,
		},
		{
			name:        "multiple observations",
			observations: []float64{0.1, 0.5, 1.0, 2.5, 5.0},
			expectedLen: 5,
		},
		{
			name:        "zero value",
			observations: []float64{0},
			expectedLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buckets := []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
			meter := observability.NewInMemoryMeter()
			hist := meter.Histogram("test_histogram", buckets)
			ctx := context.Background()
			labels := observability.Labels{}

			for _, obs := range tt.observations {
				hist.Observe(ctx, obs, labels)
			}

			samples := meter.HistogramSamples("test_histogram", labels)
			if len(samples) != tt.expectedLen {
				t.Errorf("Histogram samples: got %d, want %d", len(samples), tt.expectedLen)
			}

			// Verify all observations are recorded
			for i, expected := range tt.observations {
				if i < len(samples) && samples[i] != expected {
					t.Errorf("Sample %d: got %f, want %f", i, samples[i], expected)
				}
			}
		})
	}
}

// TestInMemoryMeter_Gauge tests gauge operations.
func TestInMemoryMeter_Gauge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		operations  []struct {
			op    string  // "Set", "Inc", "Dec"
			value float64
		}
		expected float64
	}{
		{
			name: "set single value",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Set", value: 10},
			},
			expected: 10,
		},
		{
			name: "set multiple times",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Set", value: 10},
				{op: "Set", value: 20},
			},
			expected: 20,
		},
		{
			name: "increment",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Set", value: 10},
				{op: "Inc", value: 1},
			},
			expected: 11,
		},
		{
			name: "decrement",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Set", value: 10},
				{op: "Dec", value: 1},
			},
			expected: 9,
		},
		{
			name: "negative value",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Set", value: -5},
			},
			expected: -5,
		},
		{
			name: "inc and dec combined",
			operations: []struct {
				op    string
				value float64
			}{
				{op: "Set", value: 0},
				{op: "Inc", value: 1},
				{op: "Inc", value: 1},
				{op: "Dec", value: 1},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meter := observability.NewInMemoryMeter()
			gauge := meter.Gauge("test_gauge")
			ctx := context.Background()
			labels := observability.Labels{}

			for _, op := range tt.operations {
				switch op.op {
				case "Set":
					gauge.Set(ctx, op.value, labels)
				case "Inc":
					gauge.Inc(ctx, labels)
				case "Dec":
					gauge.Dec(ctx, labels)
				}
			}

			result := meter.GaugeValue("test_gauge", labels)
			if math.Abs(result-tt.expected) > 0.0001 {
				t.Errorf("Gauge value: got %f, want %f", result, tt.expected)
			}
		})
	}
}

// TestInMemoryMeter_MultipleLabels tests metrics with different label combinations.
func TestInMemoryMeter_MultipleLabels(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	counter := meter.Counter("requests", "status", "method")
	ctx := context.Background()

	labels1 := observability.Labels{"status": "200", "method": "GET"}
	labels2 := observability.Labels{"status": "404", "method": "GET"}
	labels3 := observability.Labels{"status": "200", "method": "POST"}

	counter.Inc(ctx, labels1)
	counter.Inc(ctx, labels1)
	counter.Inc(ctx, labels2)
	counter.Inc(ctx, labels3)

	if val1 := meter.CounterValue("requests", labels1); val1 != 2 {
		t.Errorf("Label1: got %f, want 2", val1)
	}
	if val2 := meter.CounterValue("requests", labels2); val2 != 1 {
		t.Errorf("Label2: got %f, want 1", val2)
	}
	if val3 := meter.CounterValue("requests", labels3); val3 != 1 {
		t.Errorf("Label3: got %f, want 1", val3)
	}
}

// TestInMemoryMeter_DropsUnregisteredLabels tests that unregistered labels are silently dropped.
func TestInMemoryMeter_DropsUnregisteredLabels(t *testing.T) {
	t.Parallel()

	meter := observability.NewInMemoryMeter()
	counter := meter.Counter("requests", "status") // Only "status" is registered
	ctx := context.Background()

	// Try to use both registered and unregistered labels
	labels := observability.Labels{
		"status": "200",
		"method": "GET", // This should be silently dropped
	}

	counter.Inc(ctx, labels)

	// Query with only registered label
	result := meter.CounterValue("requests", observability.Labels{"status": "200"})
	if result != 1 {
		t.Errorf("Counter should count despite unregistered label, got %f, want 1", result)
	}
}

// TestNopMeter tests that NopMeter operations don't error.
func TestNopMeter(t *testing.T) {
	t.Parallel()

	meter := observability.NopMeter{}
	ctx := context.Background()
	labels := observability.Labels{}

	// These should not panic or error
	counter := meter.Counter("counter")
	counter.Inc(ctx, labels)
	counter.Add(ctx, 5, labels)

	histogram := meter.Histogram("histogram", []float64{0.1, 1.0, 10.0})
	histogram.Observe(ctx, 0.5, labels)

	gauge := meter.Gauge("gauge")
	gauge.Set(ctx, 10, labels)
	gauge.Inc(ctx, labels)
	gauge.Dec(ctx, labels)
}
