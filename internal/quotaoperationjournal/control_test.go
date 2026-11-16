package quotaoperationjournal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestBootstrapUsesControlLockAndActualReadinessWithoutOperationIdentity(t *testing.T) {
	f := freshFixture(t)
	cfg := f.config()
	bootstrapped := false
	readyCalls := 0
	authority := strings.Repeat("7", 64)
	cfg.CheckReady = func(context.Context) (string, error) {
		if !bootstrapped {
			return "", errors.New("readiness before bootstrap")
		}
		if readyCalls == 0 {
			if _, err := os.Lstat(filepath.Join(f.Directory, "operations", "indexed-v2", "journal.db")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("DB published before first actual readiness: %v", err)
			}
		}
		readyCalls++
		return authority, nil
	}
	init, err := newInitializer(FreshConfig{Control: cfg, Authorize: func(context.Context) error { return nil }}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	var retained FreshLedger
	c, err := init.InitializeFresh(context.Background(), func(_ context.Context, ledger FreshLedger) error {
		retained = ledger
		dev, ino, err := ledger.Identity()
		if err != nil || dev == 0 || ino == 0 {
			t.Fatalf("exclusive fixed ledger observation: %d %d %v", dev, ino, err)
		}
		lock, err := os.OpenFile(filepath.Join(f.Directory, "operations.lock"), os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer lock.Close()
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			return errors.New("bootstrap outside operations lock")
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		bootstrapped = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if readyCalls < 2 || c.authority != authority {
		t.Fatalf("actual digest not retained: calls%d authority%s", readyCalls, c.authority)
	}
	if _, _, err = retained.Identity(); !errors.Is(err, ErrAuthority) {
		t.Fatalf("fresh capability escaped callback:%v", err)
	}
	if _, err = c.Operation(context.Background(), f.Identity, func(quotaoperation.Identity) error { return nil }); !errors.Is(err, ErrAuthority) {
		t.Fatalf("prebootstrap identity authority admitted:%v", err)
	}
	i := f.Identity
	i.Authority = authority
	s, err := c.Operation(context.Background(), i, func(got quotaoperation.Identity) error {
		if got != i {
			return ErrAuthority
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := s.Cancel(context.Background())
	if err != nil || proof.Identity != i || !proof.Cancelled {
		t.Fatalf("actual authority scoped proof:%+v %v", proof, err)
	}
}

func TestFailedBootstrapAndReadinessRetainPartialFormatWithoutDatabase(t *testing.T) {
	for _, kind := range []string{"bootstrap", "readiness", "invalid digest"} {
		t.Run(kind, func(t *testing.T) {
			f := freshFixture(t)
			cfg := f.config()
			failure := errors.New("exact bootstrap/readiness refusal")
			readyCalls := 0
			cfg.CheckReady = func(context.Context) (string, error) {
				readyCalls++
				if kind == "readiness" {
					return "", failure
				}
				return "not-a-digest", nil
			}
			init, err := newInitializer(FreshConfig{Control: cfg, Authorize: func(context.Context) error { return nil }}, f.owner)
			if err != nil {
				t.Fatal(err)
			}
			var capability FreshLedger
			_, err = init.InitializeFresh(context.Background(), func(_ context.Context, ledger FreshLedger) error {
				capability = ledger
				if kind == "bootstrap" {
					return failure
				}
				return nil
			})
			expected := failure
			if kind == "invalid digest" {
				expected = ErrAuthority
			}
			if !errors.Is(err, expected) {
				t.Fatalf("wrong causal refusal:%v want%v", err, expected)
			}
			if kind == "bootstrap" && readyCalls != 0 {
				t.Fatalf("bootstrap failure consulted readiness:%d", readyCalls)
			}
			if _, _, err = capability.Identity(); !errors.Is(err, ErrAuthority) {
				t.Fatalf("failed capability survived:%v", err)
			}
			evidence := capture(t, f.formatPath())
			if _, err = os.Lstat(filepath.Join(f.Directory, "operations")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("premature indexed metadata:%v", err)
			}
			if _, err = openExisting(context.Background(), f.config(), f.owner); !errors.Is(err, ErrAuthority) {
				t.Fatalf("partial format adopted:%v", err)
			}
			if _, err = init.InitializeFresh(context.Background(), func(context.Context, FreshLedger) error { t.Fatal("partial retried bootstrap"); return nil }); !errors.Is(err, os.ErrExist) {
				t.Fatalf("partial reinitialize:%v", err)
			}
			evidence.unchanged(t)
		})
	}
}

func TestGenericControlHasNoOperationRowsOrIdentity(t *testing.T) {
	scoped := initializedFixture(t)
	c := scoped.control
	effects := 0
	if err := c.Run(context.Background(), func() error { effects++; return nil }); err != nil {
		t.Fatal(err)
	}
	backend := journalStore{Directory: c.config.Directory, owner: c.owner, Check: func() error { return c.check(context.Background()) }}
	if err := backend.withinExisting(context.Background(), func(j *Journal) error {
		if _, _, err := j.Lookup(scoped.identity); !errors.Is(err, ErrAuthority) {
			t.Fatalf("generic control adopted an operation:%v", err)
		}
		var count int
		if err := j.db.QueryRow(`SELECT count(*) FROM identities`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("generic action created %d operation rows", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if effects != 1 {
		t.Fatalf("fixed effect count%d", effects)
	}
}

func TestGenericAndScopedControlShareEffectLock(t *testing.T) {
	scoped := initializedFixture(t)
	c := scoped.control
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background(), func() error { close(entered); <-release; return nil }) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generic did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	proof, err := scoped.Cancel(ctx)
	close(release)
	genericErr := <-done
	if !errors.Is(err, context.DeadlineExceeded) || proof != (quotaoperation.Proof{}) || genericErr != nil {
		t.Fatalf("scoped bypassed generic:%+v %v generic%v", proof, err, genericErr)
	}
	if _, err = scoped.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestControlFreezesConfigAndRechecksActualReadyDigest(t *testing.T) {
	f := freshFixture(t)
	cfg := f.config()
	authority := f.Identity.Authority
	cfg.CheckReady = func(context.Context) (string, error) { return authority, nil }
	init, err := newInitializer(FreshConfig{Control: cfg, Authorize: func(context.Context) error { return nil }}, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	c, err := init.InitializeFresh(context.Background(), func(context.Context, FreshLedger) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	cfg.Directory = f.Directory + "-other"
	cfg.CheckControl = func(context.Context) error { return errors.New("replaced caller config") }
	effects := 0
	if err = c.Run(context.Background(), func() error { effects++; return nil }); err != nil {
		t.Fatalf("mutable caller config affected retained control:%v", err)
	}
	authority = strings.Repeat("f", 64)
	if err = c.Run(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 1 {
		t.Fatalf("changed actual authority ran effect:%v count%d", err, effects)
	}
}

func TestZeroHandlesAndMissingFreshAuthorizationRefuse(t *testing.T) {
	effects := 0
	if err := (&Control{}).Run(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
		t.Fatalf("zero control:%v count%d", err, effects)
	}
	if _, err := (&Initializer{}).InitializeFresh(context.Background(), func(context.Context, FreshLedger) error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
		t.Fatalf("zero initializer:%v count%d", err, effects)
	}
	f := freshFixture(t)
	if _, err := newInitializer(FreshConfig{Control: f.config()}, f.owner); !errors.Is(err, ErrAuthority) {
		t.Fatalf("missing first-install provenance:%v", err)
	}
	if _, err := os.Lstat(f.formatPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid initializer published anchor:%v", err)
	}
	if _, _, err := (FreshLedger{}).Identity(); !errors.Is(err, ErrAuthority) {
		t.Fatalf("raw fresh capability:%v", err)
	}
}
