package observability

import "time"

// Clock abstracts time.Now so domain & use-cases stay deterministic in tests.
//
// Calling time.Now() directly inside an aggregate constructor (e.g.
// NewOrder) makes the constructor non-deterministic and forces every test
// to compare timestamps with a tolerance. The fix is to inject Clock at
// the use-case boundary and forward the result into the constructor:
//
//   func NewOrder(id OrderID, customer CustomerID, now time.Time) *Order
//
// In production, RealClock is wired at the composition root. In tests,
// FixedClock or FakeClock returns a stable time, so assertions like
// `assert.Equal(t, expected, order.CreatedAt())` are stable.
type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// FixedClock always returns the same time. Use in unit tests.
type FixedClock struct{ T time.Time }

func (c FixedClock) Now() time.Time { return c.T }

// FakeClock allows controlled advancement; useful for testing schedulers and
// outbox workers.
type FakeClock struct{ t time.Time }

func NewFakeClock(t time.Time) *FakeClock     { return &FakeClock{t: t} }
func (c *FakeClock) Now() time.Time           { return c.t }
func (c *FakeClock) Advance(d time.Duration)  { c.t = c.t.Add(d) }
func (c *FakeClock) Set(t time.Time)          { c.t = t }