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

// These callbacks model adapter authority only, not XFS/hoststorage conformance.
type formatFixture struct {
	Directory string
	Identity  quotaoperation.Identity
	Check     func(quotaoperation.Identity) error
	owner     uint32
}

func freshFixture(t *testing.T) *formatFixture {
	t.Helper()
	raw, i := fixture(t)
	if err := os.Remove(raw.Directory); err != nil {
		t.Fatal(err)
	}
	return &formatFixture{Directory: raw.Directory, Identity: i, owner: raw.owner, Check: func(got quotaoperation.Identity) error {
		if got != i {
			return ErrAuthority
		}
		return nil
	}}
}
func (s *formatFixture) formatPath() string { return formatPath(s.Directory) }
func (s *formatFixture) config() Config {
	check := s.Check
	identity := s.Identity
	return Config{Directory: s.Directory, CheckControl: func(context.Context) error { return check(identity) }, CheckReady: func(context.Context) (string, error) { return identity.Authority, nil }}
}
func (s *formatFixture) InitializeFresh(ctx context.Context) (*Store, error) {
	check := s.Check
	identity := s.Identity
	init, err := newInitializer(FreshConfig{Control: s.config(), Authorize: func(context.Context) error { return check(identity) }}, s.owner)
	if err != nil {
		return nil, err
	}
	control, err := init.InitializeFresh(ctx, func(_ context.Context, f FreshLedger) error { _, _, err := f.Identity(); return err })
	if err != nil {
		return nil, err
	}
	return control.Operation(ctx, identity, check)
}
func (s *formatFixture) OpenExisting(ctx context.Context) (*Store, error) {
	control, err := openExisting(ctx, s.config(), s.owner)
	if err != nil {
		return nil, err
	}
	return control.Operation(ctx, s.Identity, s.Check)
}

func TestExplicitFreshFormatAndExistingFacade(t *testing.T) {
	s := freshFixture(t)
	opened, err := s.InitializeFresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	if err = opened.Apply(context.Background(), func() error { effects++; return nil }); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.OpenExisting(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	proof, err := reopened.Cancel(context.Background())
	if err != nil || !proof.Cancelled || proof.Identity != s.Identity || effects != 1 {
		t.Fatalf("fresh/existing facade: proof=%+v err=%v effects=%d", proof, err, effects)
	}
	if _, err = s.InitializeFresh(context.Background()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second initialization: %v", err)
	}
}

func TestChangedReadyAuthorityRefusesWithReinitializeGuidance(t *testing.T) {
	s := freshFixture(t)
	if _, err := s.InitializeFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := s.config()
	cfg.CheckReady = func(context.Context) (string, error) { return strings.Repeat("ab", 32), nil }
	_, err := openExisting(context.Background(), cfg, s.owner)
	if !errors.Is(err, ErrAuthority) || !errors.Is(err, ErrReadyAuthority) || !strings.Contains(err.Error(), "Reinitialize the indexed ledger") {
		t.Fatalf("changed ready authority: %v", err)
	}
	cfg.CheckReady = func(context.Context) (string, error) { return "not-a-digest", nil }
	if _, err = openExisting(context.Background(), cfg, s.owner); err != ErrAuthority {
		t.Fatalf("invalid current authority: %v", err)
	}
	if _, err = s.OpenExisting(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalEmptyLedgerIsNotFresh(t *testing.T) {
	s := freshFixture(t)
	if err := os.Mkdir(s.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InitializeFresh(context.Background()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("historical empty ledger admitted: %v", err)
	}
	if _, err := os.Lstat(s.formatPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("historical ledger created anchor: %v", err)
	}
	if _, err := s.OpenExisting(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unanchored ledger adopted: %v", err)
	}
}

func TestInterruptedInitializationRetainsPermanentRefusal(t *testing.T) {
	s := freshFixture(t)
	original := s.Check
	cut := errors.New("installer authority revoked after ledger creation")
	s.Check = func(i quotaoperation.Identity) error {
		if _, err := os.Lstat(s.Directory); err == nil {
			return cut
		}
		return original(i)
	}
	if _, err := s.InitializeFresh(context.Background()); !errors.Is(err, cut) {
		t.Fatalf("expected exact initialization boundary refusal: %v", err)
	}
	before, err := os.ReadFile(s.formatPath())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(s.formatPath())
	if err != nil {
		t.Fatal(err)
	}
	s.Check = original
	if _, err = s.OpenExisting(context.Background()); !errors.Is(err, ErrAuthority) {
		t.Fatalf("partial anchor opened: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(s.Directory, "operations.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial installation performed journal effects: %v", err)
	}
	if err = os.Rename(s.Directory, s.Directory+"-retained"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.InitializeFresh(context.Background()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("retained partial anchor reinitialized: %v", err)
	}
	after, err := os.ReadFile(s.formatPath())
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.Lstat(s.formatPath())
	if err != nil || !os.SameFile(info, now) || string(before) != string(after) {
		t.Fatalf("partial anchor evidence changed: %v", err)
	}
}

func TestRetainedAnchorNeverRecreatesLostLedger(t *testing.T) {
	s := freshFixture(t)
	if _, err := s.InitializeFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.Directory, s.Directory+"-retained"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InitializeFresh(context.Background()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("lost history recreated: %v", err)
	}
	if _, err := os.Lstat(s.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost ledger path changed: %v", err)
	}
	if err := os.Mkdir(s.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenExisting(context.Background()); !errors.Is(err, ErrAuthority) {
		t.Fatalf("replacement ledger inode adopted: %v", err)
	}
}

func TestAnchoredFacadeRefusesReplacedAnchorBeforeEffects(t *testing.T) {
	s := freshFixture(t)
	opened, err := s.InitializeFresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.formatPath())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(s.formatPath(), s.formatPath()+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.formatPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	effects := 0
	if err = opened.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
		t.Fatalf("replaced anchor allowed effects: %v count=%d", err, effects)
	}
}

func TestMissingIndexedDatabaseCannotReinitialize(t *testing.T) {
	s := freshFixture(t)
	if _, err := s.InitializeFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Directory, "operations", "indexed-v2", "journal.db")
	if err := os.Rename(path, filepath.Join(filepath.Dir(s.Directory), "retained-database")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenExisting(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing indexed database opened: %v", err)
	}
	if _, err := s.InitializeFresh(context.Background()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("missing indexed database initialized: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database recreated: %v", err)
	}
}

func TestCompleteAnchorPinsDatabaseAndLockAcrossReopen(t *testing.T) {
	for _, name := range []string{"operations.lock", "operations/indexed-v2/journal.db"} {
		t.Run(name, func(t *testing.T) {
			s := freshFixture(t)
			if _, err := s.InitializeFresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.Directory, filepath.FromSlash(name))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(path, filepath.Join(filepath.Dir(s.Directory), "retained-original")); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = s.OpenExisting(context.Background()); !errors.Is(err, ErrAuthority) {
				t.Fatalf("byte-identical replacement adopted: %v", err)
			}
		})
	}
}

func TestFreshInitializationRefusesUnsafeParentAndCancellation(t *testing.T) {
	t.Run("parent mode", func(t *testing.T) {
		s := freshFixture(t)
		if err := os.Chmod(filepath.Dir(s.Directory), 0770); err != nil {
			t.Fatal(err)
		}
		if _, err := s.InitializeFresh(context.Background()); !errors.Is(err, ErrAuthority) {
			t.Fatalf("parent custody: %v", err)
		}
		if _, err := os.Lstat(s.formatPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe parent created anchor: %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		s := freshFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.InitializeFresh(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled initializer: %v", err)
		}
		if _, err := os.Lstat(s.formatPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cancelled initializer created anchor: %v", err)
		}
	})
}

func TestFormatAnchorCustodyAndCodecRefusals(t *testing.T) {
	for _, name := range []string{"mode", "hardlink", "symlink", "future", "extra"} {
		t.Run(name, func(t *testing.T) {
			s := freshFixture(t)
			if _, err := s.InitializeFresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			path := s.formatPath()
			expected := ErrAuthority
			switch name {
			case "mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+"-retained"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-retained", path); err != nil {
					t.Fatal(err)
				}
				expected = syscall.ELOOP
			case "future":
				if err := os.WriteFile(path, []byte("{\"version\":\"future\"}\n{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "extra":
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("{}\n")
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("append: %v close:%v", err, closeErr)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.OpenExisting(context.Background()); !errors.Is(err, expected) {
				t.Fatalf("%s refusal: %v want %v", name, err, expected)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
				t.Fatalf("refusal changed evidence: %v", err)
			}
		})
	}
}
