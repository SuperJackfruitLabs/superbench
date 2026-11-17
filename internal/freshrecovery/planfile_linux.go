//go:build linux

package freshrecovery

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The reviewed plan and the retained approval of one cleanup, in
// LEDGER/payload-cleanups. The plan may be replaced until the approval exists.
func planName(key string) string     { return key + ".plan.json" }
func approvalName(key string) string { return key + ".json" }

// replacePlan publishes raw as dir/KEY.plan.json, atomically replacing an
// earlier plan, but only while no retained approval KEY.json exists. A plan is
// not authority: the effect accepts it only by its exact SHA-256. The caller
// serializes publication (operations.lock and the allocation lock).
func replacePlan(dir, key string, raw []byte, owner uint32, limit int) error {
	if len(raw) == 0 || len(raw) > limit {
		return ErrRecovery
	}
	if _, err := os.Lstat(filepath.Join(dir, approvalName(key))); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrRecovery, err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	temp := "." + planName(key) + ".new-" + hex.EncodeToString(random[:])
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer unix.Close(dfd)
	fd, err := unix.Openat(dfd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return &os.PathError{Op: "create", Path: filepath.Join(dir, temp), Err: err}
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir, temp))
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = unix.Renameat(dfd, temp, dfd, planName(key))
	}
	if err != nil {
		_ = unix.Unlinkat(dfd, temp, 0)
		return err
	}
	if err = unix.Fsync(dfd); err != nil {
		return err
	}
	got, err := readRetained(dir, planName(key), owner, limit)
	if err != nil {
		return err
	}
	if string(got) != string(raw) {
		return ErrRecovery
	}
	return nil
}

// readRetained reads dir/name only if it is a regular 0600 file of owner with
// one link, at most limit bytes, the same file by name and by fd, unchanged
// while read. A missing file is os.ErrNotExist.
func readRetained(dir, name string, owner uint32, limit int) ([]byte, error) {
	path := filepath.Join(dir, name)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var before, after, named unix.Stat_t
	if err = unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o7777 != 0o600 || before.Uid != owner || before.Nlink != 1 || before.Size > int64(limit) {
		return nil, ErrRecovery
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if err = unix.Fstat(fd, &after); err == nil {
		err = unix.Lstat(path, &named)
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > limit || int64(len(raw)) != before.Size || after.Size != before.Size || after.Mtim != before.Mtim || after.Ino != before.Ino || named.Dev != before.Dev || named.Ino != before.Ino {
		return nil, ErrRecovery
	}
	return raw, nil
}

// reviewed returns the cleanup approval bytes with SHA-256 sha: the retained
// KEY.json when it exists (retained reports so), otherwise the published plan.
// A retained approval with other bytes never yields to a newer plan.
func reviewed(dir, key, sha string, owner uint32, limit int) (raw []byte, retained bool, err error) {
	raw, err = readRetained(dir, approvalName(key), owner, limit)
	if err == nil {
		if digest(raw) != sha {
			return nil, true, ErrRecovery
		}
		return raw, true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	raw, err = readRetained(dir, planName(key), owner, limit)
	if err != nil {
		return nil, false, errors.Join(ErrRecovery, err)
	}
	if digest(raw) != sha {
		return nil, false, ErrRecovery
	}
	return raw, false, nil
}
