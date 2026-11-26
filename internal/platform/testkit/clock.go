// Package testkit holds the helpers that make tests fast, deterministic and
// honest: a clock a test moves by hand, a request builder, and — behind the
// integration build tag — real Postgres, NATS and Redis containers.
package testkit

import (
	"sync"
	"time"
)

// Clock is a fake clock. Time-dependent code takes a func() time.Time or a
// clock interface, so a test can advance a day without sleeping.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock fixed at now.
func NewClock(now time.Time) *Clock {
	return &Clock{now: now}
}

// Now returns the clock's current time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
