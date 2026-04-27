package observability

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
)

// Meter is the metrics façade exposed to use-cases.
//
// Cardinality discipline is built into the API: label keys are fixed at
// registration time, so a use-case author CANNOT accidentally pass a
// high-cardinality value (order_id, user_id) as a new label.
type Meter interface {
	Counter(name string, labelKeys ...string) Counter
	Histogram(name string, buckets []float64, labelKeys ...string) Histogram
	Gauge(name string, labelKeys ...string) Gauge
}

type Counter interface {
	Inc(ctx context.Context, labels Labels)
	Add(ctx context.Context, v float64, labels Labels)
}

type Histogram interface {
	Observe(ctx context.Context, v float64, labels Labels)
}

type Gauge interface {
	Set(ctx context.Context, v float64, labels Labels)
	Inc(ctx context.Context, labels Labels)
	Dec(ctx context.Context, labels Labels)
}

// Labels is a typed map that requires explicit construction at call sites,
// making cardinality reviews easy in code review.
type Labels map[string]string

// ----------------------------------------------------------------------------
// InMemoryMeter — testable, dependency-free implementation. In production
// swap with a Prometheus or OTel adapter; the interface stays.
// ----------------------------------------------------------------------------

type InMemoryMeter struct {
	mu         sync.Mutex
	counters   map[string]*memCounter
	histograms map[string]*memHistogram
	gauges     map[string]*memGauge
}

func NewInMemoryMeter() *InMemoryMeter {
	return &InMemoryMeter{
		counters:   map[string]*memCounter{},
		histograms: map[string]*memHistogram{},
		gauges:     map[string]*memGauge{},
	}
}

func (m *InMemoryMeter) Counter(name string, keys ...string) Counter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.counters[name]; ok {
		return c
	}
	c := &memCounter{name: name, keys: keys, values: map[string]*uint64{}}
	m.counters[name] = c
	return c
}

func (m *InMemoryMeter) Histogram(name string, buckets []float64, keys ...string) Histogram {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.histograms[name]; ok {
		return h
	}
	h := &memHistogram{name: name, keys: keys, buckets: buckets, samples: map[string][]float64{}}
	m.histograms[name] = h
	return h
}

func (m *InMemoryMeter) Gauge(name string, keys ...string) Gauge {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.gauges[name]; ok {
		return g
	}
	g := &memGauge{name: name, keys: keys, values: map[string]float64{}}
	m.gauges[name] = g
	return g
}

// CounterValue returns the recorded count for a given label set; testing aid.
//
// Returns float64 because Counter.Add accepts float64 (e.g. fractional
// byte counts). Holds the per-counter mutex (NOT just m.mu) to avoid a
// data race against concurrent writers in Counter.Add — they use
// different mutexes.
func (m *InMemoryMeter) CounterValue(name string, labels Labels) float64 {
	m.mu.Lock()
	c, ok := m.counters[name]
	m.mu.Unlock()
	if !ok {
		return 0
	}
	c.mu.Lock()
	p, ok := c.values[labelKey(c.keys, labels)]
	c.mu.Unlock()
	if !ok {
		return 0
	}
	return math.Float64frombits(atomic.LoadUint64(p))
}

func (m *InMemoryMeter) HistogramSamples(name string, labels Labels) []float64 {
	m.mu.Lock()
	h, ok := m.histograms[name]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]float64(nil), h.samples[labelKey(h.keys, labels)]...)
}

// GaugeValue is the symmetric helper for tests.
func (m *InMemoryMeter) GaugeValue(name string, labels Labels) float64 {
	m.mu.Lock()
	g, ok := m.gauges[name]
	m.mu.Unlock()
	if !ok {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.values[labelKey(g.keys, labels)]
}

// ---- internals ----

type memCounter struct {
	name   string
	keys   []string
	mu     sync.Mutex
	values map[string]*uint64 // float64 bit-pattern; CAS-updated
}

func (c *memCounter) Inc(ctx context.Context, labels Labels) { c.Add(ctx, 1, labels) }

// Add increments the counter. Counters are monotonic by definition, so a
// negative or NaN delta is a programming error: panic loudly rather than
// silently corrupt SLO math (the previous uint64(v) cast wrapped negatives
// to ~1.8e19 — see Bug 2 in the audit).
func (c *memCounter) Add(_ context.Context, v float64, labels Labels) {
	if math.IsNaN(v) || v < 0 {
		panic(fmt.Sprintf("observability: counter %q got non-monotonic delta %v", c.name, v))
	}
	if v == 0 {
		return
	}
	k := labelKey(c.keys, labels)

	c.mu.Lock()
	p, ok := c.values[k]
	if !ok {
		var z uint64
		p = &z
		c.values[k] = p
	}
	c.mu.Unlock()

	for {
		old := atomic.LoadUint64(p)
		next := math.Float64bits(math.Float64frombits(old) + v)
		if atomic.CompareAndSwapUint64(p, old, next) {
			return
		}
	}
}

type memHistogram struct {
	name    string
	keys    []string
	buckets []float64
	mu      sync.Mutex
	samples map[string][]float64
}

func (h *memHistogram) Observe(_ context.Context, v float64, labels Labels) {
	k := labelKey(h.keys, labels)
	h.mu.Lock()
	h.samples[k] = append(h.samples[k], v)
	h.mu.Unlock()
}

type memGauge struct {
	name   string
	keys   []string
	mu     sync.Mutex
	values map[string]float64
}

func (g *memGauge) Set(_ context.Context, v float64, labels Labels) {
	k := labelKey(g.keys, labels)
	g.mu.Lock()
	g.values[k] = v
	g.mu.Unlock()
}
func (g *memGauge) Inc(ctx context.Context, labels Labels) { g.delta(labels, 1) }
func (g *memGauge) Dec(ctx context.Context, labels Labels) { g.delta(labels, -1) }
func (g *memGauge) delta(labels Labels, d float64) {
	k := labelKey(g.keys, labels)
	g.mu.Lock()
	g.values[k] += d
	g.mu.Unlock()
}

// labelKey produces a deterministic key. We iterate registered keys (NOT the
// caller's map) so unregistered labels are silently dropped — preventing
// cardinality explosion.
func labelKey(keys []string, labels Labels) string {
	if len(keys) == 0 {
		return ""
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	var b []byte
	for _, k := range sorted {
		b = append(b, k...)
		b = append(b, '=')
		b = append(b, labels[k]...)
		b = append(b, ';')
	}
	return string(b)
}

// ----------------------------------------------------------------------------
// NopMeter — for unit tests that do not assert on metrics.
// ----------------------------------------------------------------------------

type NopMeter struct{}

func (NopMeter) Counter(string, ...string) Counter             { return nopCounter{} }
func (NopMeter) Histogram(string, []float64, ...string) Histogram { return nopHistogram{} }
func (NopMeter) Gauge(string, ...string) Gauge                 { return nopGauge{} }

type nopCounter struct{}

func (nopCounter) Inc(context.Context, Labels)            {}
func (nopCounter) Add(context.Context, float64, Labels)   {}

type nopHistogram struct{}

func (nopHistogram) Observe(context.Context, float64, Labels) {}

type nopGauge struct{}

func (nopGauge) Set(context.Context, float64, Labels) {}
func (nopGauge) Inc(context.Context, Labels)          {}
func (nopGauge) Dec(context.Context, Labels)          {}