package quotaoperationjournal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestInspectNeverReservesOrRevivesOriginalIdentity(t *testing.T) {
	for _, state := range []string{"unknown", "reserved", "denied"} {
		t.Run(state, func(t *testing.T) {
			s := initializedFixture(t)
			if state == "reserved" {
				if err := s.Apply(context.Background(), func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			} else if state == "denied" {
				if _, err := s.Cancel(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(s.control.config.Directory, "operations", "indexed-v2")
			db, journal := capture(t, filepath.Join(dir, "journal.db")), capture(t, filepath.Join(dir, "journal.db-journal"))
			calls := 0
			want := Observation{Identity: s.identity, Found: state != "unknown", Denied: state == "denied"}
			for attempt := 0; attempt < 2; attempt++ {
				if err := s.Inspect(context.Background(), func(got Observation) error {
					calls++
					if got != want {
						t.Fatalf("wrong retained state: %+v want %+v", got, want)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatalf("reader calls: %d", calls)
			}
			db.unchanged(t)
			journal.unchanged(t)
			if state == "denied" {
				effects := 0
				if err := s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, quotaoperation.ErrCancelled) || effects != 0 {
					t.Fatalf("inspection revived denied effects: %v count%d", err, effects)
				}
			}
		})
	}
}

func TestInspectHoldsExistingLockThroughReaderAndPropagatesError(t *testing.T) {
	s := initializedFixture(t)
	want := errors.New("retained evidence reader refused")
	err := s.Inspect(context.Background(), func(Observation) error {
		f, err := os.OpenFile(filepath.Join(s.control.config.Directory, "operations.lock"), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			t.Fatal("reader ran outside existing operation lock")
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			t.Fatal(err)
		}
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("reader refusal lost: %v", err)
	}
	// The reader's error released the same lock and did not reserve the identity.
	if err = s.Inspect(context.Background(), func(got Observation) error {
		if got.Found || got.Denied {
			t.Fatalf("failed reader changed history: %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInspectRefusesForeignIdentityBeforeReader(t *testing.T) {
	s := initializedFixture(t)
	if err := s.Apply(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"epoch", "state", "binary"} {
		t.Run(field, func(t *testing.T) {
			foreign := *s
			switch field {
			case "epoch":
				foreign.identity.Epoch = strings.Repeat("f", 32)
			case "state":
				foreign.identity.StateIdentity = strings.Repeat("f", 64)
			case "binary":
				foreign.identity.BinarySHA256 = strings.Repeat("f", 64)
			}
			foreign.verify = func(quotaoperation.Identity) error { return nil }
			calls := 0
			if err := foreign.Inspect(context.Background(), func(Observation) error { calls++; return nil }); !errors.Is(err, ErrAuthority) || calls != 0 {
				t.Fatalf("foreign identity reached reader: %v count%d", err, calls)
			}
		})
	}
}

func TestInspectRefusesLostAuthorityBeforeAndAfterReader(t *testing.T) {
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			s := initializedFixture(t)
			valid := when != "before"
			want := errors.New("original authority revoked")
			s.verify = func(quotaoperation.Identity) error {
				if !valid {
					return want
				}
				return nil
			}
			calls := 0
			err := s.Inspect(context.Background(), func(Observation) error { calls++; valid = false; return nil })
			expectedCalls := 0
			if when == "after" {
				expectedCalls = 1
			}
			if !errors.Is(err, want) || calls != expectedCalls {
				t.Fatalf("revoked inspection acknowledged: %v count%d", err, calls)
			}
		})
	}
}

func TestInspectRawMissingAndCancelledContextsNeverCallReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := initializedFixture(t)
	for _, tc := range []struct {
		name string
		s    *Store
		ctx  context.Context
		want error
	}{
		{"nil", nil, context.Background(), ErrAuthority},
		{"raw", &Store{}, context.Background(), ErrAuthority},
		{"cancelled", s, ctx, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			if err := tc.s.Inspect(tc.ctx, func(Observation) error { calls++; return nil }); !errors.Is(err, tc.want) || calls != 0 {
				t.Fatalf("invalid inspection reached reader: %v count%d", err, calls)
			}
		})
	}
	if err := s.Inspect(context.Background(), nil); !errors.Is(err, ErrAuthority) {
		t.Fatalf("nil reader: %v", err)
	}
	if err := os.Remove(filepath.Join(s.control.config.Directory, "operations", "indexed-v2", "journal.db")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := s.Inspect(context.Background(), func(Observation) error { calls++; return nil }); !errors.Is(err, os.ErrNotExist) || calls != 0 {
		t.Fatalf("missing history reached reader: %v count%d", err, calls)
	}
}
