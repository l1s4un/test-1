// Package main is a minimal composition root that wires the observability
// library the way a real service would. It is intentionally small — the
// library's value is the design (Clean-Architecture-respecting decorators,
// cardinality discipline, deterministic time), not this glue file.
//
// Run with:  go run .
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"test-1/observability"
)

// fakeUseCase stands in for a real interactor (e.g. CreateOrder). It is
// the only piece an application owner has to write — observability is
// applied around it via decorators.
type createOrderReq struct {
	CustomerID int64
	Email      string
	AmountCents int64
}

type createOrderResp struct {
	OrderID int64
}

type createOrderUC struct {
	clock observability.Clock
	// repo, paymentClient, etc. would go here.
}

func (uc *createOrderUC) Execute(ctx context.Context, req createOrderReq) (createOrderResp, error) {
	if req.AmountCents <= 0 {
		return createOrderResp{}, errors.New("amount must be positive")
	}
	// Domain work would happen here. Note: no logging, no tracing, no metrics
	// imports in the use-case body.
	_ = uc.clock.Now()
	return createOrderResp{OrderID: 42}, nil
}

func main() {
	ctx := context.Background()

	// ---- 1. Build the primitives.
	log := observability.NewSlogLogger(observability.SlogConfig{
		Writer:  os.Stdout,
		Level:   slog.LevelInfo,
		Service: "orders",
		PIIKeys: []string{"email", "card_number", "phone", "ssn"},
	})

	tracer := observability.NewInMemoryTracer() // swap with OTel adapter in prod
	meter := observability.NewInMemoryMeter()   // swap with Prometheus adapter in prod
	clock := observability.RealClock{}

	// Wire the rng-failure hook to the structured logger so a degraded
	// trace-id stream is at least visible in logs.
	observability.OnRandFailure = func(err error) {
		log.Warn(ctx, "trace id rng degraded", observability.String("err", err.Error()))
	}

	// ---- 2. Build the use-case and stack the cross-cutting decorators.
	uc := &createOrderUC{clock: clock}

	logged := observability.LoggedExecutor[createOrderReq, createOrderResp]{
		Inner: uc, Log: log, Name: "CreateOrder", Clock: clock,
	}
	metered := observability.NewMeteredExecutor[createOrderReq, createOrderResp](
		logged, meter, "CreateOrder",
	)
	traced := observability.TracedExecutor[createOrderReq, createOrderResp]{
		Inner: metered, Tracer: tracer, Name: "usecase.CreateOrder",
	}

	// ---- 3. Exercise the stack twice — once happy, once with a bad input.
	_, _ = traced.Execute(ctx, createOrderReq{
		CustomerID: 1, Email: "alice@example.com", AmountCents: 9999,
	})
	if _, err := traced.Execute(ctx, createOrderReq{CustomerID: 1, AmountCents: 0}); err != nil {
		// Outer error handling (HTTP 400, etc.) lives in the transport layer.
		_ = err
	}

	// ---- 4. Print a small summary so a reader can see the wiring works.
	log.Info(ctx, "summary",
		observability.Int64("traces_recorded", int64(len(tracer.Snapshot()))),
		observability.Int64("usecase_success_total", int64(meter.CounterValue(
			"usecase_total",
			observability.Labels{"operation": "CreateOrder", "status": "success"},
		))),
		observability.Int64("usecase_failure_total", int64(meter.CounterValue(
			"usecase_total",
			observability.Labels{"operation": "CreateOrder", "status": "failure"},
		))),
		observability.String("started_at", clock.Now().Format(time.RFC3339)),
	)
}