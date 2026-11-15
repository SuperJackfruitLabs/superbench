package repaircontrol

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReplacedLockCannotAdoptPriorOpenDescriptorAdmissionDomain(t *testing.T) {
	j, a := fixture(t)
	if err := syscall.Flock(int(j.lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(a.Control.State, lockName)
	if err := os.Rename(path, path+"-preserved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	other, err := OpenExisting(a.Control.State, a.Instance)
	if err == nil {
		other.Close()
		t.Fatal("replacement lock formed a second admission domain while original descriptor was held")
	}
}

func TestCopiedDatabaseAndMovedOriginalFilesCannotAdoptReplacedRoot(t *testing.T) {
	for _, kind := range []string{"copied-db", "replaced-root"} {
		t.Run(kind, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(parent, "state")
			if err := os.Mkdir(state, 0700); err != nil {
				t.Fatal(err)
			}
			j, err := Create(state)
			if err != nil {
				t.Fatal(err)
			}
			instance := j.Instance()
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "copied-db" {
				path := filepath.Join(state, databaseName)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(state, state+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{databaseName, lockName, identityName} {
					if err := os.Rename(filepath.Join(state+"-preserved", name), filepath.Join(state, name)); err != nil {
						t.Fatal(err)
					}
				}
			}
			other, err := OpenExisting(state, instance)
			if err == nil {
				other.Close()
				t.Fatal("exact prior authority adopted under replaced physical identity")
			}
		})
	}
}
