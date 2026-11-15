package gitcontrol

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// NoOperation is a read-only absence proof, not a fence. Callers must hold the
// host session and durably block further admission of this exact target. Any
// unattributed or unknown journal prevents proof; unrelated attributed journals
// remain untouched, even if their managed roots are currently unavailable.
func NoOperation(state string, t operationcontrol.Target) error {
	if !t.Valid() {
		return operationcontrol.ErrAuthority
	}
	info, err := os.Lstat(state)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return operationcontrol.ErrAuthority
	}
	physical, err := filepath.EvalSymlinks(state)
	if err != nil {
		return err
	}
	if physical != state {
		return operationcontrol.ErrAuthority
	}
	check := func() error {
		now, err := os.Lstat(state)
		if err != nil {
			return err
		}
		if !os.SameFile(info, now) {
			return operationcontrol.ErrAuthority
		}
		return nil
	}
	dir, err := controlDir(state, false)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return check()
	}
	if err != nil {
		return err
	}
	defer f.Close()
	count := 0
	for {
		entries, e := f.ReadDir(128)
		if e != nil && e != io.EOF {
			return e
		}
		for _, entry := range entries {
			count++
			name := entry.Name()
			if count > 1000 || !strings.HasSuffix(name, ".json") || !controlID.MatchString(strings.TrimSuffix(name, ".json")) {
				return operationcontrol.ErrAuthority
			}
			j, err := readControl(dir, name)
			if err != nil {
				return err
			}
			if j.OperationID == "" || j.OperationID == t.ID() {
				return operationcontrol.ErrAuthority
			}
		}
		if e == io.EOF {
			break
		}
	}
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !os.SameFile(opened, named) {
		return operationcontrol.ErrAuthority
	}
	return check()
}
