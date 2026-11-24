package deadlinetest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpireEndsDerivedContextsAsADeadline(t *testing.T) {
	c := New()
	child, cancel := context.WithTimeout(c, time.Minute)
	defer cancel()
	if c.Err() != nil || child.Err() != nil {
		t.Fatal("expired before Expire")
	}
	c.Expire()
	c.Expire()
	<-child.Done()
	if !errors.Is(child.Err(), context.DeadlineExceeded) || !errors.Is(c.Err(), context.DeadlineExceeded) {
		t.Fatal(child.Err(), c.Err())
	}
}

type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Fatal(...any)          { r.failed = true }
func (r *recorder) Fatalf(string, ...any) { r.failed = true }

func TestBoundedAcceptsOnlyTheBoundUnderTest(t *testing.T) {
	from := time.Now()
	bounded, cancel := context.WithTimeout(New(), time.Minute)
	defer cancel()
	to := time.Now()
	later, cancelLater := context.WithTimeout(context.Background(), time.Minute)
	defer cancelLater()
	for name, c := range map[string]struct {
		ctx  context.Context
		fail bool
	}{"bound": {bounded, false}, "caller's": {New(), true}, "none": {context.Background(), true}, "fresh": {later, true}} {
		r := &recorder{TB: t}
		Bounded(r, c.ctx, from, to, time.Minute)
		if r.failed != c.fail {
			t.Errorf("%s: failed=%v", name, r.failed)
		}
	}
}
