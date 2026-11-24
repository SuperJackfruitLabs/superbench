// Package deadlinetest helps tests prove which step a deadline covers without
// racing the host's speed. Test-only: no production package imports it.
//
// A wall-clock deadline short enough to fire at the step under test also fires
// during the real git and database setup before it when the host is loaded, so
// it cannot prove which step it covered. Instead, give the operation a bound
// long enough for setup, check with Bounded that the step sees that bound, and
// end the step with Expire once the test reaches it.
package deadlinetest

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Expiring is a caller context whose expiry the test triggers. Its deadline is
// always an hour away, so it never masks a shorter bound under test.
type Expiring struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func New() *Expiring {
	return &Expiring{Context: context.Background(), done: make(chan struct{})}
}

func (c *Expiring) Deadline() (time.Time, bool) { return time.Now().Add(time.Hour), true }
func (c *Expiring) Done() <-chan struct{}       { return c.done }

// Err reports context.DeadlineExceeded once Expire has run, as a real deadline would.
func (c *Expiring) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// Expire ends the context and every context derived from it.
func (c *Expiring) Expire() { c.once.Do(func() { close(c.done) }) }

// Bounded fails unless ctx carries the deadline timeout after a moment between
// from and to: the bound under test, rather than a caller's distant one, none
// at all, or a fresh one started later.
func Bounded(t testing.TB, ctx context.Context, from, to time.Time, timeout time.Duration) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("unbounded context")
	}
	if deadline.Before(from.Add(timeout)) || deadline.After(to.Add(timeout)) {
		t.Fatalf("deadline %v is not %v after a moment between %v and %v", deadline, timeout, from, to)
	}
}
