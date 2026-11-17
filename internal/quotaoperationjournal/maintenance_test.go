package quotaoperationjournal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestMaintenanceRequiresPermanentDenialAndKeepsOriginalLock(t *testing.T) {
	for _, state := range []string{"unknown", "reserved", "denied"} {
		t.Run(state, func(t *testing.T) {
			s := initializedFixture(t)
			if state == "reserved" {
				if err := s.Apply(context.Background(), func() error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if state == "denied" {
				if _, err := s.Cancel(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(s.control.config.Directory, "operations", "indexed-v2")
			db, journal := capture(t, filepath.Join(dir, "journal.db")), capture(t, filepath.Join(dir, "journal.db-journal"))
			calls := 0
			want := errors.New("maintenance effect failed after durable approval")
			err := s.MaintainDenied(context.Background(), func() error {
				calls++
				f, err := os.OpenFile(filepath.Join(s.control.config.Directory, "operations.lock"), os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
				if err == nil {
					syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					t.Fatal("effect outside operations.lock")
				}
				if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
					t.Fatal(err)
				}
				return want
			})
			if state == "denied" {
				if calls != 1 || !errors.Is(err, want) {
					t.Fatalf("effect error lost: calls%d %v", calls, err)
				}
				if err = s.MaintainDenied(context.Background(), func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if err = s.Apply(context.Background(), func() error { t.Fatal("maintenance revived identity"); return nil }); !errors.Is(err, quotaoperation.ErrCancelled) {
					t.Fatal(err)
				}
			} else if calls != 0 || !errors.Is(err, ErrAuthority) {
				t.Fatalf("unapproved effect: calls%d %v", calls, err)
			}
			db.unchanged(t)
			journal.unchanged(t)
		})
	}
}

func TestMaintenanceRefusesLostAuthorityAndInvalidHandles(t *testing.T) {
	s := initializedFixture(t)
	if _, err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	valid := true
	want := errors.New("maintenance authority revoked")
	s.verify = func(quotaoperation.Identity) error {
		if !valid {
			return want
		}
		return nil
	}
	err := s.MaintainDenied(context.Background(), func() error { valid = false; return nil })
	if !errors.Is(err, want) {
		t.Fatalf("revoked effect acknowledged: %v", err)
	}
	for _, store := range []*Store{nil, {}, s} {
		if err := store.MaintainDenied(context.Background(), nil); !errors.Is(err, ErrAuthority) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.MaintainDenied(ctx, func() error { t.Fatal("cancelled effect ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestMaintenanceFailedDenialDurabilityPreventsEffect(t *testing.T) {
	s := initializedFixture(t)
	if _, err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend, err := s.backend(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("denial sync failed")
	calls := 0
	err = backend.withinExisting(context.Background(), func(j *Journal) error {
		j.durable = func() error { return want }
		return j.maintainDenied(s.identity, func() error { calls++; return nil })
	})
	if !errors.Is(err, want) || calls != 0 {
		t.Fatalf("maintenance ran without durable denial: calls%d %v", calls, err)
	}
}
