// Package clock provides a testable time abstraction.
package clock

import "time"

// Clock provides the current time. Use Real for production and Fake for tests.
type Clock interface {
	Now() time.Time
}

// Real delegates to time.Now.
type Real struct{}

// Now returns the current time.
func (Real) Now() time.Time { return time.Now() }

// Fake returns a fixed time, adjustable by tests.
type Fake struct {
	Time time.Time
}

// Now returns the fake's fixed time.
func (f *Fake) Now() time.Time { return f.Time }

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) { f.Time = f.Time.Add(d) }

// Fixed reports one moment forever: the time a decision was made. Hand it to
// code that measures an age so every age in one pass is read against the same
// instant, not against a wall clock that keeps moving while the pass runs.
// Production code uses it; Fake is for tests that need to move time.
type Fixed struct {
	Time time.Time
}

// Now returns the fixed instant.
func (f Fixed) Now() time.Time { return f.Time }
