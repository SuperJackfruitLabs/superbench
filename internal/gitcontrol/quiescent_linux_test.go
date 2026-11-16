//go:build linux

package gitcontrol

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func quietFixture(t *testing.T, journal bool) string {
	t.Helper()
	base := canonicalTemp(t)
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if journal {
		if err := os.Mkdir(filepath.Join(state, "git-controls"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	q, err := Quiescent(state)
	if err != nil {
		t.Fatalf("valid custody baseline: %v", err)
	}
	if err = q.Recheck(); err != nil {
		t.Fatalf("valid pinned baseline: %v", err)
	}
	if err = q.Close(); err != nil {
		t.Fatal(err)
	}
	return state
}
func requireQuietRefusal(t *testing.T, state string, cause error) {
	t.Helper()
	q, err := Quiescent(state)
	if q != nil || !errors.Is(err, ErrQuiescent) || cause != nil && !errors.Is(err, cause) {
		t.Fatalf("expected precise custody refusal (cause %v), got handle=%v err=%v", cause, q, err)
	}
	if err.Error() != ErrQuiescent.Error() {
		t.Fatal("private filesystem cause escaped bounded error")
	}
}
func TestQuiescentAbsentOrEmptyInventoryIsReadOnlyAndClosed(t *testing.T) {
	for _, journal := range []bool{false, true} {
		state := quietFixture(t, journal)
		q, err := Quiescent(state)
		if err != nil {
			t.Fatal(err)
		}
		if err = q.Recheck(); err != nil {
			t.Fatal(err)
		}
		if err = q.Close(); err != nil {
			t.Fatal(err)
		}
		if err = q.Recheck(); !errors.Is(err, ErrQuiescent) {
			t.Fatalf("closed observation reused: %v", err)
		}
		if err = q.Close(); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(state)
		if err != nil {
			t.Fatal(err)
		}
		if !journal && len(entries) != 0 || journal && (len(entries) != 1 || entries[0].Name() != "git-controls") {
			t.Fatal("read-only gate created or changed state")
		}
	}
	var zero Quiescence
	if err := zero.Recheck(); !errors.Is(err, ErrQuiescent) {
		t.Fatal("zero observation admitted")
	}
	var nilHandle *Quiescence
	if err := nilHandle.Recheck(); !errors.Is(err, ErrQuiescent) {
		t.Fatal("nil observation admitted")
	}
}
func TestQuiescentRefusesEveryPendingOrUnknownEntryWithoutOpeningIt(t *testing.T) {
	for _, kind := range []string{"attributed", "legacy", "unknown", "FIFO", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			state := quietFixture(t, true)
			dir := filepath.Join(state, "git-controls")
			path := filepath.Join(dir, "unknown")
			switch kind {
			case "attributed", "legacy":
				path, j := fixtureIntent(t, state, state)
				if kind == "attributed" {
					j.OperationID = strings.Repeat("a", 64)
					j.OperationEpoch = strings.Repeat("b", 32)
				} else {
					j.Version = legacyControlVersion
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := saveControl(dir, j); err != nil {
					t.Fatal(err)
				}
				if got, err := readControl(dir, filepath.Base(path)); err != nil || got != j {
					t.Fatalf("valid retained journal fixture: %v", err)
				}
			case "unknown":
				if err := os.WriteFile(path, []byte("retained evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			case "FIFO":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("retained-target-must-not-open", path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadDir(dir)
			if err != nil || len(before) != 1 {
				t.Fatalf("causal nonempty fixture: %v", err)
			}
			info, err := os.Lstat(filepath.Join(dir, before[0].Name()))
			if err != nil {
				t.Fatal(err)
			}
			requireQuietRefusal(t, state, nil)
			after, err := os.Lstat(filepath.Join(dir, before[0].Name()))
			if err != nil || !quietSame(info, after, true) {
				t.Fatalf("refusal changed original journal evidence: %v", err)
			}
		})
	}
}
func TestQuiescentRefusesMissingOrExposedCustody(t *testing.T) {
	for _, kind := range []string{"missing-state", "state-mode", "journal-mode", "state-symlink", "ancestor-symlink", "journal-symlink", "journal-FIFO"} {
		t.Run(kind, func(t *testing.T) {
			state := quietFixture(t, true)
			dir := filepath.Join(state, "git-controls")
			candidate := state
			var cause error
			switch kind {
			case "missing-state":
				candidate = filepath.Join(filepath.Dir(state), "absent")
				cause = os.ErrNotExist
			case "state-mode":
				if err := os.Chmod(state, 0755); err != nil {
					t.Fatal(err)
				}
			case "journal-mode":
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			case "state-symlink", "ancestor-symlink":
				link := filepath.Join(canonicalTemp(t), "link")
				target := state
				if kind == "ancestor-symlink" {
					target = filepath.Dir(state)
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				candidate = link
				if kind == "ancestor-symlink" {
					candidate = filepath.Join(link, "state")
				}
				cause = syscall.ENOTDIR
			case "journal-symlink", "journal-FIFO":
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				if kind == "journal-symlink" {
					if err := os.Symlink("missing-private-target", dir); err != nil {
						t.Fatal(err)
					}
				} else if err := syscall.Mkfifo(dir, 0600); err != nil {
					t.Fatal(err)
				}
				cause = syscall.ENOTDIR
			}
			requireQuietRefusal(t, candidate, cause)
		})
	}
}
func TestQuiescentRecheckPinsStateAncestorsAndJournalHistory(t *testing.T) {
	for _, kind := range []string{"journal-replacement", "state-replacement", "ancestor-replacement", "journal-mode", "journal-created", "journal-created-removed", "pending-created-removed"} {
		t.Run(kind, func(t *testing.T) {
			initialJournal := kind != "journal-created" && kind != "journal-created-removed"
			state := quietFixture(t, initialJournal)
			dir := filepath.Join(state, "git-controls")
			q, err := Quiescent(state)
			if err != nil {
				t.Fatal(err)
			}
			defer q.Close()
			switch kind {
			case "journal-replacement":
				if err = os.Rename(dir, filepath.Join(filepath.Dir(state), "retained-journal")); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			case "state-replacement":
				if err = os.Rename(state, filepath.Join(filepath.Dir(state), "retained-state")); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			case "ancestor-replacement":
				parent := filepath.Dir(state)
				backup := parent + "-retained"
				if err = os.Rename(parent, backup); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if e := os.Rename(state, filepath.Join(backup, "state")); e != nil {
						t.Error(e)
					}
					if e := os.Remove(parent); e != nil {
						t.Error(e)
					}
					if e := os.Rename(backup, parent); e != nil {
						t.Error(e)
					}
				}()
				if err = os.Mkdir(parent, 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(filepath.Join(backup, "state"), state); err != nil {
					t.Fatal(err)
				}
			case "journal-mode":
				if err = os.Chmod(dir, 0750); err != nil {
					t.Fatal(err)
				}
			case "journal-created", "journal-created-removed":
				if err = os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "journal-created-removed" {
					if err = os.Remove(dir); err != nil {
						t.Fatal(err)
					}
					info, e := os.Stat(state)
					if e != nil {
						t.Fatal(e)
					}
					stamp := info.ModTime().Add(time.Second)
					if err = os.Chtimes(state, stamp, stamp); err != nil {
						t.Fatal(err)
					}
				}
			case "pending-created-removed":
				path := filepath.Join(dir, "retained-during-observation")
				if err = os.WriteFile(path, []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
				info, e := os.Stat(dir)
				if e != nil {
					t.Fatal(e)
				}
				stamp := info.ModTime().Add(time.Second)
				if err = os.Chtimes(dir, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if err = q.Recheck(); !errors.Is(err, ErrQuiescent) {
				t.Fatalf("replaced or changed observation adopted: %v", err)
			}
		})
	}
}
func TestQuiescentReleasesDescriptorsOnSuccessAndRefusal(t *testing.T) {
	state := quietFixture(t, true)
	count := func() int {
		t.Helper()
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	baseline := count()
	for i := 0; i < 20; i++ {
		q, err := Quiescent(state)
		if err != nil {
			t.Fatal(err)
		}
		if err = q.Recheck(); err != nil {
			t.Fatal(err)
		}
		if err = q.Close(); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(state, "git-controls", "unknown")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		requireQuietRefusal(t, state, nil)
	}
	if after := count(); after != baseline {
		t.Fatalf("owned descriptors leaked: before%d after%d", baseline, after)
	}
}
