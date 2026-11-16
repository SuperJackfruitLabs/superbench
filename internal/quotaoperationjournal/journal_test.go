package quotaoperationjournal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func fixture(t *testing.T) (Store, quotaoperation.Identity) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "ledger")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	i := quotaoperation.Identity{OperationID: strings.Repeat("a", 64), Epoch: strings.Repeat("b", 32), StateIdentity: strings.Repeat("c", 64), Authority: strings.Repeat("d", 64), BinarySHA256: strings.Repeat("e", 64)}
	s := Store{Directory: dir, Identity: i, owner: uint32(os.Geteuid()), Check: func(got quotaoperation.Identity) error {
		if got != i {
			return ErrAuthority
		}
		return nil
	}}
	return s, i
}
func create(t *testing.T, s Store) {
	t.Helper()
	if err := s.Create(context.Background(), func(*Journal) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestPermanentHistoryDoesNotExhaustNewIdentities(t *testing.T) {
	s, i := fixture(t)
	create(t, s)
	err := s.Within(context.Background(), func(j *Journal) error {
		// Seed valid permanent history in one real durable transaction. The public
		// admission below must remain indexed after the old lifetime ceiling.
		tx, e := j.db.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		for n := 0; n < 30001; n++ {
			old := i
			old.OperationID = fmt.Sprintf("%064x", n)
			raw, _ := rawIdentity(old)
			if _, e = tx.Exec(`INSERT INTO identities VALUES(?,?,?)`, old.OperationID, raw, n%2); e != nil {
				return e
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return j.Reserve(i)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 30000} {
		old := i
		old.OperationID = fmt.Sprintf("%064x", n)
		historical := s
		historical.Identity = old
		historical.Check = func(got quotaoperation.Identity) error {
			if got != old {
				return ErrAuthority
			}
			return nil
		}
		if err = historical.Within(context.Background(), func(j *Journal) error {
			found, denied, e := j.Lookup(old)
			if e != nil {
				return e
			}
			if !found || denied != (n%2 == 1) {
				return errors.New("historical identity changed")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPendingNeverAuthorizesOrOverwritesAnIdentity(t *testing.T) {
	s, i := fixture(t)
	create(t, s)
	err := s.Within(context.Background(), func(j *Journal) error {
		name := "pending-" + i.OperationID + "-" + i.Epoch + "-123"
		if e := j.RetainPending(name, i); e != nil {
			return e
		}
		found, _, e := j.Lookup(i)
		if e != nil {
			return e
		}
		if found {
			return errors.New("pending became canonical")
		}
		foreign := i
		foreign.Epoch = strings.Repeat("f", 32)
		if _, _, e = j.Lookup(foreign); !errors.Is(e, ErrAuthority) {
			return errors.New("pending identity was substituted")
		}
		if e = j.Deny(i); e != nil {
			return e
		}
		if e = j.Reserve(i); !errors.Is(e, quotaoperation.ErrCancelled) {
			return errors.New("denied identity admitted")
		}
		var count int
		if e = j.db.QueryRow(`SELECT count(*) FROM pending WHERE name=?`, name).Scan(&count); e != nil {
			return e
		}
		if count != 1 {
			return errors.New("pending evidence removed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFullPendingBudgetStillPermitsKnownDenial(t *testing.T) {
	s, i := fixture(t)
	create(t, s)
	err := s.Within(context.Background(), func(j *Journal) error {
		if e := j.Reserve(i); e != nil {
			return e
		}
		tx, e := j.db.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		raw, _ := rawIdentity(i)
		for n := 0; n < MaxPending; n++ {
			name := fmt.Sprintf("pending-%s-%s-%d", i.OperationID, i.Epoch, n)
			if _, e = tx.Exec(`INSERT INTO pending VALUES(?,?,?)`, name, i.OperationID, raw); e != nil {
				return e
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		if e = j.RetainPending("pending-"+i.OperationID+"-"+i.Epoch+"-10001", i); !errors.Is(e, ErrCapacity) {
			return errors.New("unresolved pending budget exceeded")
		}
		return j.Deny(i)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJournalCannotEscapeLockCallback(t *testing.T) {
	s, i := fixture(t)
	var saved *Journal
	if e := s.Create(context.Background(), func(j *Journal) error { saved = j; return nil }); e != nil {
		t.Fatal(e)
	}
	if e := saved.Reserve(i); !errors.Is(e, ErrAuthority) {
		t.Fatal("unlocked journal escaped", e)
	}
}

func TestAuthorityAndNamespaceAreRecheckedBeforeAdmission(t *testing.T) {
	for _, kind := range []string{"policy", "unknown", "lock"} {
		t.Run(kind, func(t *testing.T) {
			s, i := fixture(t)
			create(t, s)
			valid := true
			s.Check = func(quotaoperation.Identity) error {
				if !valid {
					return ErrAuthority
				}
				return nil
			}
			err := s.Within(context.Background(), func(j *Journal) error {
				switch kind {
				case "policy":
					valid = false
				case "unknown":
					if e := os.WriteFile(filepath.Join(s.Directory, "operations", "unknown"), []byte("preserve"), 0600); e != nil {
						return e
					}
				case "lock":
					p := filepath.Join(s.Directory, "operations.lock")
					if e := os.Rename(p, p+".retained"); e != nil {
						return e
					}
					if e := os.WriteFile(p, nil, 0600); e != nil {
						return e
					}
				}
				if e := j.Reserve(i); !errors.Is(e, ErrAuthority) {
					t.Errorf("changed authority admitted: %v", e)
				}
				return ErrAuthority
			})
			if !errors.Is(err, ErrAuthority) {
				t.Fatal(err)
			}
		})
	}
}

func TestSameOperationsLockBoundsWaiting(t *testing.T) {
	s, _ := fixture(t)
	create(t, s)
	if e := s.Within(context.Background(), func(*Journal) error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		e := s.Within(ctx, func(*Journal) error { t.Error("second worker bypassed ledger lock"); return nil })
		if !errors.Is(e, context.DeadlineExceeded) {
			return fmt.Errorf("lock wait: %w", e)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestMissingNamedLockNeverCreatesASecondLockDomain(t *testing.T) {
	s, i := fixture(t)
	create(t, s)
	err := s.Within(context.Background(), func(j *Journal) error {
		// This callback still holds the original inode's flock after unlink.
		path := filepath.Join(s.Directory, "operations.lock")
		if e := os.Remove(path); e != nil {
			return e
		}
		e := s.Within(context.Background(), func(*Journal) error { t.Error("replacement lock domain adopted"); return nil })
		if !errors.Is(e, os.ErrNotExist) {
			t.Errorf("missing lock was recreated: %v", e)
		}
		if _, e = os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
			t.Error("normal reopen created lock", e)
		}
		if e = j.Reserve(i); !errors.Is(e, ErrAuthority) {
			t.Error("unlinked old lock admitted effects", e)
		}
		return ErrAuthority
	})
	if !errors.Is(err, ErrAuthority) {
		t.Fatal(err)
	}
}

func TestCallbackCannotSubstituteIdentityUnderAnotherAuthorityCheck(t *testing.T) {
	s, i := fixture(t)
	create(t, s)
	if e := s.Within(context.Background(), func(j *Journal) error {
		other := i
		other.OperationID = strings.Repeat("f", 64)
		if e := j.Reserve(other); !errors.Is(e, ErrAuthority) {
			t.Error("unchecked identity reserved", e)
		}
		if e := j.Deny(other); !errors.Is(e, ErrAuthority) {
			t.Error("unchecked identity denied", e)
		}
		if e := j.RetainPending("pending-"+other.OperationID+"-"+other.Epoch+"-1", other); !errors.Is(e, ErrAuthority) {
			t.Error("unchecked identity retained", e)
		}
		var count int
		if e := j.db.QueryRow(`SELECT count(*) FROM identities`).Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return errors.New("foreign identity persisted")
		}
		return j.Reserve(i)
	}); e != nil {
		t.Fatal(e)
	}
}
