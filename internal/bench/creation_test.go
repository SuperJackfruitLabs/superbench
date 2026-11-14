package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreationRegistrationReservesInitializationBeforeLaunch(t *testing.T) {
	s, remote := fixture(t)
	seed := filepath.Join(filepath.Dir(remote), "seed")
	if e := os.Mkdir(filepath.Join(seed, ".bench"), 0700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(seed, ".bench/config.toml"), "[setup]\nonce='exit 7'\n")
	g(t, seed, "add", ".")
	g(t, seed, "commit", "-m", "initial setup")
	g(t, seed, "push", "origin", "main")
	b, warming, e := s.createRegistered(context.Background(), "task", Options{Repo: remote}, false, true)
	if e != nil || !warming || b.Lease == nil || b.Lease.Continuation == "" || b.Lease.Execution != nil {
		t.Fatal(b, warming, e)
	}
	// A new controller observes the reservation even if the creating process
	// disappeared before it could launch setup. Creation must not redispatch it.
	restarted := Store{State: s.State, Data: s.Data}
	if _, e = restarted.Lease("task", "competitor"); !errors.Is(e, ErrBusy) {
		t.Fatal("ownership gap before setup", e)
	}
	if _, e = restarted.CreateContext(context.Background(), "task", Options{Repo: remote}, false); !errors.Is(e, ErrExists) {
		t.Fatal("initialization blindly repeated", e)
	}
	history, e := restarted.History("task", 100)
	if e != nil || len(history) != 0 {
		t.Fatal("reservation fabricated an execution", history, e)
	}
	stopped, e := restarted.Stop("task", "", true)
	if e != nil || stopped.Lease == nil || stopped.Lease.Continuation != "" {
		t.Fatal(stopped, e)
	}
	if _, e = restarted.Checkpoint("task", b.Lease.Token); e != nil {
		t.Fatal(e)
	}
	if e = restarted.Release("task", b.Lease.Token); e != nil {
		t.Fatal(e)
	}
}
