package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// ----------------------------------------------------------------------------
// noopTracer
// ----------------------------------------------------------------------------

type noopTracer struct{}

// NewNoopTracer returns a Tracer that does nothing. Useful for unit tests
// where we don't care about traces and don't want to depend on a real
// exporter.
func NewNoopTracer() Tracer { return noopTracer{} }

func (noopTracer) Start(ctx context.Context, _ string, _ ...Attr) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttributes(...Attr)        {}
func (noopSpan) AddEvent(string, ...Attr)     {}
func (noopSpan) RecordError(error)            {}
func (noopSpan) End()                         {}
func (noopSpan) TraceID() string              { return "" }
func (noopSpan) SpanID() string               { return "" }

// ----------------------------------------------------------------------------
// inMemoryTracer — simple W3C-id generating tracer used in tests AND as the
// default fallback. In production you would replace this with an OTel adapter
// (see otel_adapter.go.example in the repo notes); the use-case layer does
// not need to know.
// ----------------------------------------------------------------------------

type ctxKey int

const spanKey ctxKey = 1

type InMemoryTracer struct {
	mu    sync.Mutex
	Spans []*RecordedSpan
}

type RecordedSpan struct {
	Name      string
	TraceID   string
	SpanID    string
	ParentID  string
	StartedAt time.Time
	EndedAt   time.Time
	Attrs     []Attr
	Events    []RecordedEvent
	Err       error
}

type RecordedEvent struct {
	Name  string
	At    time.Time
	Attrs []Attr
}

func NewInMemoryTracer() *InMemoryTracer { return &InMemoryTracer{} }

func (t *InMemoryTracer) Start(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	parent, _ := ctx.Value(spanKey).(*recSpan)
	rs := &RecordedSpan{
		Name:      name,
		TraceID:   traceIDFromParent(parent),
		SpanID:    randHex(8),
		StartedAt: time.Now(),
		Attrs:     append([]Attr(nil), attrs...),
	}
	if parent != nil {
		rs.ParentID = parent.rec.SpanID
	}
	s := &recSpan{tracer: t, rec: rs}

	t.mu.Lock()
	t.Spans = append(t.Spans, rs)
	t.mu.Unlock()

	return context.WithValue(ctx, spanKey, s), s
}

// SpanFromContext lets infra adapters (e.g. logging middleware) read the
// active span without owning the tracer. Returns a no-op span if none.
func SpanFromContext(ctx context.Context) Span {
	if s, ok := ctx.Value(spanKey).(*recSpan); ok && s != nil {
		return s
	}
	return noopSpan{}
}

type recSpan struct {
	tracer *InMemoryTracer
	rec    *RecordedSpan
	once   sync.Once
}

func (s *recSpan) SetAttributes(attrs ...Attr) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	s.rec.Attrs = append(s.rec.Attrs, attrs...)
}

func (s *recSpan) AddEvent(name string, attrs ...Attr) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	s.rec.Events = append(s.rec.Events, RecordedEvent{
		Name: name, At: time.Now(), Attrs: append([]Attr(nil), attrs...),
	})
}

func (s *recSpan) RecordError(err error) {
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	s.rec.Err = err
}

func (s *recSpan) End() {
	s.once.Do(func() {
		s.tracer.mu.Lock()
		s.rec.EndedAt = time.Now()
		s.tracer.mu.Unlock()
	})
}

func (s *recSpan) TraceID() string { return s.rec.TraceID }
func (s *recSpan) SpanID() string  { return s.rec.SpanID }

func traceIDFromParent(p *recSpan) string {
	if p != nil {
		return p.rec.TraceID
	}
	return randHex(16)
}

// Snapshot returns a copy of the recorded spans. Use this in tests instead
// of reading Spans directly while spans may still be open — RecordedSpan
// fields are mutated under t.mu by SetAttributes/AddEvent/End, so a direct
// `tr.Spans[0].Attrs` read races with concurrent writers.
func (t *InMemoryTracer) Snapshot() []*RecordedSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*RecordedSpan, len(t.Spans))
	copy(out, t.Spans)
	return out
}

// OnRandFailure is invoked when crypto/rand.Read returns an error.
//
// crypto/rand can fail (rare, but observed on Windows under entropy
// starvation and in seccomp-restricted containers). Without this hook the
// previous implementation silently emitted all-zero IDs, collapsing every
// span in the process into a single trace. Wire it from the composition
// root to your structured logger:
//
//   observability.OnRandFailure = func(err error) {
//       log.Warn(context.Background(), "trace id rng degraded",
//           observability.String("err", err.Error()))
//   }
var OnRandFailure = func(error) {}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		OnRandFailure(err)
		// Degraded fallback: time-based pseudo-id. Not cryptographically
		// secure but at least each call produces a different value, so we
		// don't lose trace separation.
		ns := uint64(time.Now().UnixNano())
		for i := 0; i < len(b); i++ {
			b[i] = byte(ns >> (8 * (i % 8)))
			ns ^= ns << 13
			ns ^= ns >> 7
			ns ^= ns << 17
		}
	}
	return hex.EncodeToString(b)
}