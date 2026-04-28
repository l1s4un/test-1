package observability_test

import (
	"testing"
	"time"

	"test-1/observability"
)

// TestFixedClock_AlwaysReturnsSameTime tests that FixedClock consistently returns the same time.
func TestFixedClock_AlwaysReturnsSameTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		t    time.Time
	}{
		{
			name: "Unix epoch",
			t:    time.Unix(0, 0).UTC(),
		},
		{
			name: "Specific date",
			t:    time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
		},
		{
			name: "With nanoseconds",
			t:    time.Date(2025, 4, 27, 10, 0, 0, 123456789, time.UTC),
		},
		{
			name: "Different timezone",
			t:    time.Date(2025, 4, 27, 10, 0, 0, 0, time.FixedZone("EST", -5*3600)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := observability.FixedClock{T: tt.t}

			// Call Now() multiple times
			result1 := clock.Now()
			result2 := clock.Now()
			result3 := clock.Now()

			// All calls should return exactly the same value
			if !result1.Equal(result2) {
				t.Errorf("First and second Now() differ: %v vs %v", result1, result2)
			}
			if !result2.Equal(result3) {
				t.Errorf("Second and third Now() differ: %v vs %v", result2, result3)
			}
			if !result1.Equal(tt.t) {
				t.Errorf("Now() returned %v, want %v", result1, tt.t)
			}
		})
	}
}

// TestFakeClock_Advance tests that FakeClock correctly advances time.
func TestFakeClock_Advance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		initial   time.Time
		advances  []time.Duration
		expected  time.Time
	}{
		{
			name:     "Single advance",
			initial:  time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			advances: []time.Duration{time.Hour},
			expected: time.Date(2025, 4, 27, 11, 0, 0, 0, time.UTC),
		},
		{
			name:     "Multiple advances",
			initial:  time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			advances: []time.Duration{time.Hour, time.Minute, time.Second},
			expected: time.Date(2025, 4, 27, 11, 1, 1, 0, time.UTC),
		},
		{
			name:     "Nanosecond precision",
			initial:  time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			advances: []time.Duration{time.Nanosecond * 500},
			expected: time.Date(2025, 4, 27, 10, 0, 0, 500, time.UTC),
		},
		{
			name:     "Negative advance (go back in time)",
			initial:  time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			advances: []time.Duration{-time.Hour},
			expected: time.Date(2025, 4, 27, 9, 0, 0, 0, time.UTC),
		},
		{
			name:     "Zero advance",
			initial:  time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			advances: []time.Duration{0},
			expected: time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := observability.NewFakeClock(tt.initial)

			for _, adv := range tt.advances {
				clock.Advance(adv)
			}

			result := clock.Now()
			if !result.Equal(tt.expected) {
				t.Errorf("After advance, got %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestFakeClock_Set tests that FakeClock correctly sets time.
func TestFakeClock_Set(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		initial     time.Time
		newTime     time.Time
	}{
		{
			name:    "Set to future time",
			initial: time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			newTime: time.Date(2025, 4, 28, 10, 0, 0, 0, time.UTC),
		},
		{
			name:    "Set to past time",
			initial: time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			newTime: time.Date(2025, 4, 26, 10, 0, 0, 0, time.UTC),
		},
		{
			name:    "Set to same time",
			initial: time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
			newTime: time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := observability.NewFakeClock(tt.initial)
			clock.Set(tt.newTime)

			result := clock.Now()
			if !result.Equal(tt.newTime) {
				t.Errorf("After Set, got %v, want %v", result, tt.newTime)
			}
		})
	}
}

// TestFakeClock_AdvanceAndSet tests that Advance and Set work together correctly.
func TestFakeClock_AdvanceAndSet(t *testing.T) {
	t.Parallel()

	initial := time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC)
	clock := observability.NewFakeClock(initial)

	// Advance 1 hour
	clock.Advance(time.Hour)
	if !clock.Now().Equal(time.Date(2025, 4, 27, 11, 0, 0, 0, time.UTC)) {
		t.Error("After advance, time is incorrect")
	}

	// Set to a new time
	newTime := time.Date(2025, 4, 28, 15, 30, 0, 0, time.UTC)
	clock.Set(newTime)
	if !clock.Now().Equal(newTime) {
		t.Error("After set, time is incorrect")
	}

	// Advance from the new time
	clock.Advance(time.Minute)
	if !clock.Now().Equal(newTime.Add(time.Minute)) {
		t.Error("After second advance, time is incorrect")
	}
}

// TestRealClock_ReturnsCurrentTime tests that RealClock returns approximately the current time.
func TestRealClock_ReturnsCurrentTime(t *testing.T) {
	t.Parallel()

	clock := observability.RealClock{}
	before := time.Now().UTC()
	result := clock.Now()
	after := time.Now().UTC()

	// Result should be between before and after (within a reasonable tolerance)
	if result.Before(before) || result.After(after.Add(time.Second)) {
		t.Errorf("RealClock returned time outside expected range: before=%v, result=%v, after=%v", before, result, after)
	}
}

// TestFakeClock_MultipleAdvancesAddUp tests that multiple small advances accumulate correctly.
func TestFakeClock_MultipleAdvancesAddUp(t *testing.T) {
	t.Parallel()

	initial := time.Date(2025, 4, 27, 10, 0, 0, 0, time.UTC)
	clock := observability.NewFakeClock(initial)

	// Advance 10 times by 1 minute each
	for i := 0; i < 10; i++ {
		clock.Advance(time.Minute)
	}

	expected := time.Date(2025, 4, 27, 10, 10, 0, 0, time.UTC)
	if !clock.Now().Equal(expected) {
		t.Errorf("After 10 minute advances, got %v, want %v", clock.Now(), expected)
	}
}
