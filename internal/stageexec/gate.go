package stageexec

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var gateKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

// Gate linearizes starts and stops independently from the host-wide state lock.
// A caller must release it before waiting for execution completion.
func Gate(state, key string, fn func() error) error {
	if !gateKey.MatchString(key) {
		return errors.New("invalid bench key")
	}
	f, e := os.OpenFile(filepath.Join(state, "execution-"+key+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		return e
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
