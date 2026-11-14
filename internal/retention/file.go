package retention

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"os"
	"path/filepath"
	"syscall"
)

func Name(id string) (string, error) {
	if !history.ID.MatchString(id) {
		return "", errors.New("invalid run id")
	}
	return "run-" + id + ".log", nil
}
func Directory(root string) error {
	st, e := os.Lstat(root)
	if e != nil {
		return e
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return errors.New("retention requires a private owned state directory")
	}
	return nil
}
func Snapshot(root, id string) (reap.Entry, error) {
	var zero reap.Entry
	if e := Directory(root); e != nil {
		return zero, e
	}
	name, e := Name(id)
	if e != nil {
		return zero, e
	}
	st, e := os.Lstat(filepath.Join(root, name))
	if e != nil {
		return zero, e
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return zero, errors.New("log must be a private owned regular file")
	}
	return reap.File(root, name)
}
func lockFile(root, id string) (*os.File, reap.Entry, error) {
	name, e := Name(id)
	if e != nil {
		return nil, reap.Entry{}, e
	}
	f, e := os.OpenFile(filepath.Join(root, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, reap.Entry{}, e
	}
	fail := func(e error) (*os.File, reap.Entry, error) { f.Close(); return nil, reap.Entry{}, e }
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fail(errors.New("log writer is still open"))
	}
	snapshot, e := Snapshot(root, id)
	if e != nil {
		return fail(e)
	}
	st, e := f.Stat()
	if e != nil {
		return fail(e)
	}
	if uint64(st.Sys().(*syscall.Stat_t).Dev) != snapshot.Device || st.Sys().(*syscall.Stat_t).Ino != snapshot.Inode {
		return fail(errors.New("log changed while locking"))
	}
	return f, snapshot, nil
}

// LockSnapshot excludes the product's log writer during reconciliation.
func LockSnapshot(root, id string) (*os.File, reap.Entry, error) { return lockFile(root, id) }
