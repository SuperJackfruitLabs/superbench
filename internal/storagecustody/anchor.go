package storagecustody

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type OwnerRecord struct{ Root, Ledger string }

// Anchor preserves the existing fixed quota owner publication and retry checks.
// It supplies no first-install or allocation authority.
func Anchor(root, ledger string) error {
	path := filepath.Join(root, ".superbench-quota-owner.json")
	expected := OwnerRecord{Root: root, Ledger: ledger}
	stage, e := os.CreateTemp(root, ".quota-owner-")
	if e != nil {
		return e
	}
	defer os.Remove(stage.Name())
	raw, e := json.Marshal(expected)
	if e != nil {
		stage.Close()
		return e
	}
	if _, e = stage.Write(raw); e != nil {
		stage.Close()
		return e
	}
	if e = stage.Sync(); e != nil {
		stage.Close()
		return e
	}
	if e = stage.Close(); e != nil {
		return e
	}
	// Publish complete contents without replacing an anchor from a competing ledger.
	if e = os.Link(stage.Name(), path); os.IsExist(e) {
		stat, e := os.Lstat(path)
		if e != nil {
			return e
		}
		if !stat.Mode().IsRegular() || stat.Sys().(*syscall.Stat_t).Uid != 0 || stat.Mode().Perm()&0022 != 0 {
			return errors.New("invalid filesystem ownership anchor")
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var actual OwnerRecord
		if e = json.Unmarshal(raw, &actual); e != nil {
			return e
		}
		if actual != expected {
			return errors.New("filesystem already belongs to another quota ledger")
		}
		return nil
	} else if e != nil {
		return e
	}
	dir, e := os.Open(root)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
