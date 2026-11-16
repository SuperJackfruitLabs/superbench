package quotaoperationjournal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type evidence struct {
	path string
	info os.FileInfo
	raw  []byte
	link string
}

func capture(t *testing.T, path string) evidence {
	t.Helper()
	info, e := os.Lstat(path)
	if e != nil {
		t.Fatal(e)
	}
	v := evidence{path: path, info: info}
	if info.Mode().IsRegular() {
		v.raw, e = os.ReadFile(path)
	} else if info.Mode()&os.ModeSymlink != 0 {
		v.link, e = os.Readlink(path)
	}
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (v evidence) unchanged(t *testing.T) {
	t.Helper()
	now := capture(t, v.path)
	a, b := v.info.Sys().(*syscall.Stat_t), now.info.Sys().(*syscall.Stat_t)
	if !os.SameFile(v.info, now.info) || v.info.Mode() != now.info.Mode() || a.Uid != b.Uid || a.Gid != b.Gid || a.Nlink != b.Nlink || !bytes.Equal(v.raw, now.raw) || v.link != now.link {
		t.Fatal("refusal changed original evidence", v.path)
	}
}
func normal(t *testing.T, s Store) {
	t.Helper()
	if e := s.create(context.Background(), func(j *Journal) error { return j.Reserve(s.Identity) }); e != nil {
		t.Fatal("valid baseline creation", e)
	}
	if e := s.withinExisting(context.Background(), func(j *Journal) error {
		found, denied, e := j.Lookup(s.Identity)
		if e != nil {
			return e
		}
		if !found || denied {
			t.Fatal("valid baseline identity missing/denied")
		}
		return nil
	}); e != nil {
		t.Fatal("valid baseline reopen", e)
	}
}
func rejectWithin(t *testing.T, s Store, want error) {
	t.Helper()
	called := false
	err := s.withinExisting(context.Background(), func(*Journal) error { called = true; return nil })
	if called {
		t.Fatal("refusal reached callback/effect boundary")
	}
	if !errors.Is(err, want) {
		t.Fatalf("wrong causal refusal: got %v, want %v", err, want)
	}
}

func TestInterruptedCreationAndUnknownMetadataRefuseWithoutRemoval(t *testing.T) {
	for _, kind := range []string{"partial", "legacy", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := fixture(t)
			normal(t, s)
			ops := filepath.Join(s.Directory, "operations")
			indexed := filepath.Join(ops, "indexed-v2")
			var retained evidence
			want := ErrAuthority
			switch kind {
			case "partial":
				// Keep the original named lock and indexed directory, so the missing
				// DB (not an unrelated missing-lock guard) must cause this refusal.
				if e := os.Remove(filepath.Join(indexed, "journal.db")); e != nil {
					t.Fatal(e)
				}
				retained = capture(t, filepath.Join(indexed, "journal.db-journal"))
				want = os.ErrNotExist
			case "legacy":
				if e := os.Rename(indexed, filepath.Join(s.Directory, "saved-indexed")); e != nil {
					t.Fatal(e)
				}
				raw, e := json.Marshal(struct {
					Version  string
					Identity any
					State    string
				}{"superbench.quota-operation.v1", s.Identity, "0"})
				if e != nil {
					t.Fatal(e)
				}
				path := filepath.Join(ops, s.Identity.OperationID+".json")
				if e = os.WriteFile(path, raw, 0600); e != nil {
					t.Fatal(e)
				}
				retained = capture(t, path)
			case "unknown":
				path := filepath.Join(ops, "unknown")
				if e := os.WriteFile(path, []byte("preserve"), 0600); e != nil {
					t.Fatal(e)
				}
				retained = capture(t, path)
			}
			called := false
			e := s.create(context.Background(), func(*Journal) error { called = true; return nil })
			if called || !errors.Is(e, ErrAuthority) {
				t.Fatal("existing namespace was adopted or refused for unrelated error", called, e)
			}
			rejectWithin(t, s, want)
			retained.unchanged(t)
			if kind == "partial" {
				if _, e = os.Lstat(filepath.Join(indexed, "journal.db")); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("partial DB reconstructed", e)
				}
			}
		})
	}
}

func TestPrivilegedOpenRefusesChangedFilesAndSchema(t *testing.T) {
	for _, fault := range []string{"symlink", "hardlink", "mode", "fifo", "future", "trigger", "replacement"} {
		t.Run(fault, func(t *testing.T) {
			s, _ := fixture(t)
			normal(t, s)
			db := filepath.Join(s.Directory, "operations", "indexed-v2", "journal.db")
			var original *evidence
			switch fault {
			case "future", "trigger":
				injected := false
				e := s.withinExisting(context.Background(), func(j *Journal) error {
					statement := `PRAGMA user_version=999`
					if fault == "trigger" {
						statement = `CREATE TRIGGER ignored BEFORE INSERT ON identities BEGIN SELECT RAISE(IGNORE); END`
					}
					if _, e := j.db.Exec(statement); e != nil {
						t.Fatal("fault injection failed", e)
					}
					injected = true
					return nil
				})
				if !injected || !errors.Is(e, ErrAuthority) {
					t.Fatal("changed schema did not hit schema authority guard", injected, e)
				}
			case "mode":
				if e := os.Chmod(db, 0644); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(db, filepath.Join(s.Directory, "alias")); e != nil {
					t.Fatal(e)
				}
			default:
				// Backup is outside the indexed namespace: replacement must reach
				// SQLite's DB guard, not fail because of an extra .saved entry.
				old := filepath.Join(s.Directory, "saved-journal.db")
				if e := os.Rename(db, old); e != nil {
					t.Fatal(e)
				}
				v := capture(t, old)
				original = &v
				if fault == "symlink" {
					if e := os.Symlink(old, db); e != nil {
						t.Fatal(e)
					}
				} else if fault == "fifo" {
					makeFIFO(t, db)
				} else {
					if e := os.WriteFile(db, []byte("not a journal"), 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			retained := capture(t, db)
			called := false
			e := s.withinExisting(context.Background(), func(*Journal) error { called = true; return nil })
			if called {
				t.Fatal("unsafe journal reached callback/effect boundary")
			}
			switch fault {
			case "symlink":
				if !errors.Is(e, syscall.ELOOP) {
					t.Fatal("symlink missed nofollow guard", e)
				}
			case "replacement":
				var code interface{ Code() int }
				if !errors.As(e, &code) || code.Code()&255 != 26 {
					t.Fatal("replacement missed SQLITE_NOTADB guard", e)
				}
			default:
				if !errors.Is(e, ErrAuthority) {
					t.Fatal("wrong causal authority refusal", fault, e)
				}
			}
			retained.unchanged(t)
			if original != nil {
				original.unchanged(t)
			}
		})
	}
}
