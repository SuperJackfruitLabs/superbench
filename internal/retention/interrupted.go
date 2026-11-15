package retention

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

// ObserveInterrupted reads an existing interrupted execution log without creating
// or replacing evidence. Its limit must be the execution's recorded log limit.
// The caller must exclude product launches and prove the execution fenced before
// calling, and retain that exclusion through its SQL commit. A returned file
// holds the product writer's advisory lock and must remain open through commit.
// Nil file and entry mean the fixed leaf was absent, not an empty captured log.
func ObserveInterrupted(root, id string, limit uint64) (*os.File, *reap.Entry, int64, error) {
	if err := interruptedInputs(root, id, limit); err != nil {
		return nil, nil, 0, err
	}
	name, _ := Name(id)
	f, err := os.OpenFile(filepath.Join(root, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// ENOENT from an open is insufficient if the parent vanished. Lstat
			// also distinguishes a dangling symlink from an absent fixed leaf.
			if err := interruptedAbsent(root, name); err != nil {
				return nil, nil, 0, err
			}
			return nil, nil, 0, nil
		}
		return nil, nil, 0, err
	}
	fail := func(err error) (*os.File, *reap.Entry, int64, error) {
		_ = f.Close()
		return nil, nil, 0, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(errors.New("interrupted log writer is still open"))
	}
	entry, size, err := interruptedSnapshot(root, name, limit, f)
	if err != nil {
		return fail(err)
	}
	return f, &entry, size, nil
}

// RecheckInterrupted repeats the bounded observation immediately before the
// caller commits. Absence has no inode to lock: launch exclusion and revocation
// must remain held throughout observe, recheck and commit. It never creates an
// empty replacement. A present observation uses the original locked descriptor.
func RecheckInterrupted(root, id string, limit uint64, f *os.File, entry *reap.Entry, size int64) error {
	if err := interruptedInputs(root, id, limit); err != nil {
		return err
	}
	name, _ := Name(id)
	if f == nil && entry == nil && size == 0 {
		return interruptedAbsent(root, name)
	}
	if f == nil || entry == nil || entry.Path != name || size < 0 || uint64(size) > limit {
		return errors.New("invalid interrupted log observation")
	}
	current, currentSize, err := interruptedSnapshot(root, name, limit, f)
	if err != nil {
		return err
	}
	if current != *entry || currentSize != size {
		return errors.New("interrupted log changed after observation")
	}
	return nil
}

func interruptedInputs(root, id string, limit uint64) error {
	if _, err := Name(id); err != nil {
		return err
	}
	// Host log configuration and recorded execution limits are at most 1 GiB.
	if limit == 0 || limit > 1<<30 {
		return errors.New("invalid interrupted log limit")
	}
	return interruptedDirectory(root)
}

func interruptedDirectory(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("interrupted log requires a normalized absolute state directory")
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if physical != root {
		return errors.New("interrupted log requires a physical state directory")
	}
	return Directory(root)
}

func interruptedAbsent(root, name string) error {
	if err := interruptedDirectory(root); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(root, name)); errors.Is(err, os.ErrNotExist) {
		// Revalidate the parent after the leaf lookup as well; a missing parent
		// cannot be converted into evidence that a log was never created.
		return interruptedDirectory(root)
	} else if err != nil {
		return err
	}
	return errors.New("interrupted log is no longer absent")
}

func interruptedStat(st os.FileInfo, limit uint64) error {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 {
		return errors.New("interrupted log must be a private owned regular file with one link")
	}
	if st.Size() < 0 || uint64(st.Size()) > limit {
		return errors.New("interrupted log exceeds its recorded limit")
	}
	return nil
}

func interruptedSnapshot(root, name string, limit uint64, f *os.File) (reap.Entry, int64, error) {
	var zero reap.Entry
	if err := interruptedDirectory(root); err != nil {
		return zero, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return zero, 0, err
	}
	if err := interruptedStat(st, limit); err != nil {
		return zero, 0, err
	}
	path := filepath.Join(root, name)
	linked, err := os.Lstat(path)
	if err != nil {
		return zero, 0, err
	}
	if !os.SameFile(st, linked) || linked.Mode() != st.Mode() || linked.Size() != st.Size() {
		return zero, 0, errors.New("interrupted log pathname changed")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return zero, 0, err
	}
	raw, git1, git256 := sha256.New(), sha1.New(), sha256.New()
	prefix := []byte(fmt.Sprintf("blob %d\x00", st.Size()))
	_, _ = git1.Write(prefix)
	_, _ = git256.Write(prefix)
	// Read at most the approved initial size plus one byte, even if an actor
	// ignoring the advisory lock grows the file while it is being hashed.
	n, copyErr := io.CopyN(io.MultiWriter(raw, git1, git256), f, st.Size()+1)
	if n != st.Size() || copyErr != io.EOF {
		return zero, 0, errors.New("interrupted log changed during bounded hashing")
	}
	final, err := f.Stat()
	if err != nil {
		return zero, 0, err
	}
	if err := interruptedStat(final, limit); err != nil {
		return zero, 0, err
	}
	linked, err = os.Lstat(path)
	if err != nil {
		return zero, 0, err
	}
	if !os.SameFile(st, final) || !os.SameFile(final, linked) || final.Mode() != st.Mode() || linked.Mode() != st.Mode() || final.Size() != st.Size() || linked.Size() != st.Size() || !final.ModTime().Equal(st.ModTime()) {
		return zero, 0, errors.New("interrupted log changed during observation")
	}
	if err := interruptedDirectory(root); err != nil {
		return zero, 0, err
	}
	sys := final.Sys().(*syscall.Stat_t)
	entry := reap.Entry{Path: name, Mode: uint32(final.Mode()), Device: uint64(sys.Dev), Inode: sys.Ino,
		SHA256: hex.EncodeToString(raw.Sum(nil)), GitSHA1: hex.EncodeToString(git1.Sum(nil)), GitSHA256: hex.EncodeToString(git256.Sum(nil))}
	return entry, st.Size(), nil
}
