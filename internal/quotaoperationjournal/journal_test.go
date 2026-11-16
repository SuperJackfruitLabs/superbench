package quotaoperationjournal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

var readbackDriverID atomic.Uint64

// This test-only driver delegates to the real SQLite driver and changes only
// the row just inserted in the same real transaction. No trigger/schema guard
// can substitute for Reserve's exact post-insert readback in this fixture.
type corruptInsertDriver struct {
	base    driver.Driver
	foreign []byte
	denied  bool
	changed *bool
}
type corruptInsertConn struct {
	driver.Conn
	fault *corruptInsertDriver
}

func (d *corruptInsertDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &corruptInsertConn{Conn: c, fault: d}, nil
}
func (c *corruptInsertConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	executor, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, errors.New("actual SQLite fixture lacks ExecerContext")
	}
	result, err := executor.ExecContext(ctx, query, args)
	if err != nil || !strings.HasPrefix(query, "INSERT INTO identities(") {
		return result, err
	}
	update := "UPDATE identities SET identity=? WHERE id=?"
	value := any(c.fault.foreign)
	if c.fault.denied {
		update = "UPDATE identities SET denied=? WHERE id=?"
		value = int64(1)
	}
	changed, err := executor.ExecContext(ctx, update, []driver.NamedValue{{Ordinal: 1, Value: value}, {Ordinal: 2, Value: args[0].Value}})
	if err != nil {
		return nil, err
	}
	n, err := changed.RowsAffected()
	if err != nil || n != 1 {
		return nil, errors.New("post-insert fault did not change exactly one row")
	}
	*c.fault.changed = true
	return result, nil
}

func TestReservationReadsBackActualInsertedIdentityAndDenial(t *testing.T) {
	for _, kind := range []string{"identity", "denied"} {
		t.Run(kind, func(t *testing.T) {
			s, i := fixture(t)
			create(t, s)
			effects := 0
			corrupted := false
			err := s.withinExisting(context.Background(), func(j *Journal) error {
				foreign := i
				foreign.Epoch = strings.Repeat("f", 32)
				raw, err := rawIdentity(foreign)
				if err != nil {
					return err
				}
				name := fmt.Sprintf("quota-readback-%d", readbackDriverID.Add(1))
				sql.Register(name, &corruptInsertDriver{base: j.db.Driver(), foreign: raw, denied: kind == "denied", changed: &corrupted})
				u := url.URL{Scheme: "file", Path: filepath.Join(s.Directory, "operations", "indexed-v2", "journal.db")}
				q := u.Query()
				q.Set("mode", "rw")
				for _, pragma := range []string{"busy_timeout(5000)", "synchronous(FULL)", "journal_mode(PERSIST)"} {
					q.Add("_pragma", pragma)
				}
				u.RawQuery = q.Encode()
				proxy, err := sql.Open(name, u.String())
				if err != nil {
					return err
				}
				proxy.SetMaxOpenConns(1)
				original := j.db
				j.db = proxy
				defer func() { j.db = original; _ = proxy.Close() }()
				if err = j.ready(); err != nil {
					return fmt.Errorf("valid SQLite baseline: %w", err)
				}
				found, _, err := j.Lookup(i)
				if err != nil || found {
					return fmt.Errorf("fresh identity baseline: found%v error%v", found, err)
				}
				err = j.Reserve(i)
				if err == nil {
					effects++
				}
				if !errors.Is(err, ErrAuthority) || !corrupted || effects != 0 {
					t.Errorf("post-insert corruption reached effect: %v corrupted%v effects%d", err, corrupted, effects)
				}
				var count int
				if err = proxy.QueryRow(`SELECT count(*) FROM identities WHERE id=?`, i.OperationID).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					t.Errorf("refused transaction retained corrupted identity: %d", count)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fixture(t *testing.T) (journalStore, quotaoperation.Identity) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("fixture parent is not private: %v", info.Mode())
	}
	dir := filepath.Join(root, "ledger")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	i := quotaoperation.Identity{OperationID: strings.Repeat("a", 64), Epoch: strings.Repeat("b", 32), StateIdentity: strings.Repeat("c", 64), Authority: strings.Repeat("d", 64), BinarySHA256: strings.Repeat("e", 64)}
	s := journalStore{Directory: dir, Identity: &i, owner: uint32(os.Geteuid()), Check: func() error { return nil }}
	return s, i
}
func create(t *testing.T, s journalStore) {
	t.Helper()
	if err := s.create(context.Background(), func(*Journal) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestPermanentHistoryDoesNotExhaustNewIdentities(t *testing.T) {
	s, i := fixture(t)
	create(t, s)
	err := s.withinExisting(context.Background(), func(j *Journal) error {
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
		historical.Identity = &old
		historical.Check = func() error { return nil }
		if err = historical.withinExisting(context.Background(), func(j *Journal) error {
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
	err := s.withinExisting(context.Background(), func(j *Journal) error {
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
	err := s.withinExisting(context.Background(), func(j *Journal) error {
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
	if e := s.create(context.Background(), func(j *Journal) error { saved = j; return nil }); e != nil {
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
			s.Check = func() error {
				if !valid {
					return ErrAuthority
				}
				return nil
			}
			err := s.withinExisting(context.Background(), func(j *Journal) error {
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
	if e := s.withinExisting(context.Background(), func(*Journal) error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		e := s.withinExisting(ctx, func(*Journal) error { t.Error("second worker bypassed ledger lock"); return nil })
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
	err := s.withinExisting(context.Background(), func(j *Journal) error {
		// This callback still holds the original inode's flock after unlink.
		path := filepath.Join(s.Directory, "operations.lock")
		if e := os.Remove(path); e != nil {
			return e
		}
		e := s.withinExisting(context.Background(), func(*Journal) error { t.Error("replacement lock domain adopted"); return nil })
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
	if e := s.withinExisting(context.Background(), func(j *Journal) error {
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
