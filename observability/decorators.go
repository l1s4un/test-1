package observability

import (
	"context"
	"fmt"
)

// This file demonstrates the recommended instrumentation pattern: DECORATORS.
//
// All three decorators are PANIC-SAFE: if the wrapped use-case panics, the
// decorator records the outcome (span error / error log / failure metric),
// then re-throws so the upstream supervisor (HTTP server, worker loop) can
// recover. Without this, production crashes are invisible in observability
// — they happen too fast for log buffers but never reach the post-call
// instrumentation.

// Executor is the minimal contract every use-case satisfies.
type Executor[Req any, Resp any] interface {
	Execute(ctx context.Context, req Req) (Resp, error)
}

// ----------------------------------------------------------------------------
// TracedExecutor
// ----------------------------------------------------------------------------

type TracedExecutor[Req any, Resp any] struct {
	Inner  Executor[Req, Resp]
	Tracer Tracer
	Name   string // span name, e.g. "usecase.CreateOrder"
}

func (t TracedExecutor[Req, Resp]) Execute(ctx context.Context, req Req) (resp Resp, err error) {
	ctx, span := t.Tracer.Start(ctx, t.Name)
	defer span.End()
	defer func() {
		if r := recover(); r != nil {
			perr := fmt.Errorf("panic: %v", r)
			span.RecordError(perr)
			span.SetAttributes(Bool("error", true), Bool("panic", true))
			panic(r) // re-throw; outer recovery middleware handles it
		}
		if err != nil {
			span.RecordError(err)
			span.SetAttributes(Bool("error", true))
		}
	}()
	resp, err = t.Inner.Execute(ctx, req)
	return resp, err
}

// ----------------------------------------------------------------------------
// LoggedExecutor — logs start, completion / failure / panic, and duration.
// ----------------------------------------------------------------------------

type LoggedExecutor[Req any, Resp any] struct {
	Inner Executor[Req, Resp]
	Log   Logger
	Name  string
	// Clock is optional; defaults to RealClock. Inject FakeClock in tests
	// that assert on duration_ms.
	Clock Clock
}

func (l LoggedExecutor[Req, Resp]) Execute(ctx context.Context, req Req) (resp Resp, err error) {
	clk := l.Clock
	if clk == nil {
		clk = RealClock{}
	}
	start := clk.Now()
	l.Log.Info(ctx, l.Name+" started")

	defer func() {
		dms := Int64("duration_ms", clk.Now().Sub(start).Milliseconds())
		if r := recover(); r != nil {
			l.Log.Error(ctx, l.Name+" panicked", fmt.Errorf("panic: %v", r), dms)
			panic(r)
		}
		if err != nil {
			l.Log.Error(ctx, l.Name+" failed", err, dms)
			return
		}
		l.Log.Info(ctx, l.Name+" completed", dms)
	}()

	resp, err = l.Inner.Execute(ctx, req)
	return resp, err
}

// ----------------------------------------------------------------------------
// MeteredExecutor — counter + duration histogram per outcome.
// Status values: "success" | "failure" | "panic".
// ----------------------------------------------------------------------------

type MeteredExecutor[Req any, Resp any] struct {
	Inner    Executor[Req, Resp]
	Counter  Counter   // labels: ["operation", "status"]
	Duration Histogram // labels: ["operation", "status"]
	Name     string
	Clock    Clock
}

// NewMeteredExecutor pre-registers metrics with the right (low-cardinality)
// label keys.
func NewMeteredExecutor[Req any, Resp any](
	inner Executor[Req, Resp], meter Meter, name string,
) MeteredExecutor[Req, Resp] {
	return MeteredExecutor[Req, Resp]{
		Inner: inner, Name: name, Clock: RealClock{},
		Counter: meter.Counter("usecase_total", "operation", "status"),
		Duration: meter.Histogram(
			"usecase_duration_seconds",
			[]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
			"operation", "status",
		),
	}
}

func (m MeteredExecutor[Req, Resp]) Execute(ctx context.Context, req Req) (resp Resp, err error) {
	clk := m.Clock
	if clk == nil {
		clk = RealClock{}
	}
	start := clk.Now()

	defer func() {
		status := "success"
		var rec any
		if rec = recover(); rec != nil {
			status = "panic"
		} else if err != nil {
			status = "failure"
		}
		labels := Labels{"operation": m.Name, "status": status}
		m.Counter.Inc(ctx, labels)
		m.Duration.Observe(ctx, clk.Now().Sub(start).Seconds(), labels)
		if rec != nil {
			panic(rec)
		}
	}()

	resp, err = m.Inner.Execute(ctx, req)
	return resp, err
}