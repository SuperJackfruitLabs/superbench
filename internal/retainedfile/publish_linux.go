//go:build linux

package retainedfile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

// ErrRefused reports a policy refusal: the caller, the directory, the name, the
// size or an existing file breaks the retained-file rules. Other errors come from
// the operating system.
var ErrRefused = errors.New("retained file refused")

const (
	tempMarker = ".tmp-"
	// maxName leaves room for the temp prefix and suffix inside NAME_MAX.
	maxName = 200
)

// DirectRoot requires a direct root caller: uid and euid 0, not under sudo.
func DirectRoot() error { return directRoot(0) }

func directRoot(uid uint32) error {
	if os.Getuid() != int(uid) || os.Geteuid() != int(uid) || os.Getenv("SUDO_UID") != "" {
		return ErrRefused
	}
	return nil
}

// Publish retains raw under dir/name exactly once. dir must be a root-owned
// private directory. An existing file is accepted only as an exact retry: a
// root-owned 0600 regular file with one link and the same bytes. It is fsynced
// again, because a retry after a lost reply must re-establish durability.
//
// Crash safety: the final name only ever appears by link() of a temp file that
// is already complete and fsynced. A crash at any point leaves either no final
// file or a complete one. Leftover dot-named temp files are harmless: their
// names are random, and a temp still linked to an exact final file is unlinked
// by the next retry before the link count is checked.
func Publish(dir, name string, raw []byte, limit int) error {
	return publish(0, func(p string) error { return storagecustody.RootOwnedDirectory(p, true) }, dir, name, raw, limit)
}

// publish is Publish for an expected owner. Production always passes uid 0 and
// the root custody check; tests pass their own uid.
func publish(owner uint32, directory func(string) error, dir, name string, raw []byte, limit int) error {
	if err := directRoot(owner); err != nil {
		return err
	}
	if !validName(name) || limit <= 0 || len(raw) == 0 || len(raw) > limit {
		return ErrRefused
	}
	if err := directory(dir); err != nil {
		return err
	}
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return pathError("open", dir, err)
	}
	defer unix.Close(dfd)
	var held unix.Stat_t
	if err = unix.Fstat(dfd, &held); err != nil {
		return pathError("stat", dir, err)
	}
	if held.Mode&unix.S_IFMT != unix.S_IFDIR || held.Uid != owner || held.Mode&0077 != 0 {
		return ErrRefused
	}
	temp, err := tempName(name)
	if err != nil {
		return err
	}
	tfd, err := unix.Openat(dfd, temp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return pathError("open", filepath.Join(dir, temp), err)
	}
	tf := os.NewFile(uintptr(tfd), filepath.Join(dir, temp))
	defer tf.Close()
	tempLinked := true
	defer func() {
		if tempLinked {
			_ = unix.Unlinkat(dfd, temp, 0)
		}
	}()
	if _, err = tf.Write(raw); err != nil {
		return err
	}
	if err = tf.Sync(); err != nil {
		return err
	}
	var want unix.Stat_t
	if err = unix.Fstat(tfd, &want); err != nil {
		return pathError("stat", tf.Name(), err)
	}
	if !retained(want, owner) {
		return ErrRefused
	}
	final := tf
	switch err = unix.Linkat(dfd, temp, dfd, name, 0); {
	case err == nil:
	case errors.Is(err, unix.EEXIST):
		if final, err = exactRetry(dfd, dir, name, raw, limit, owner); err != nil {
			return err
		}
		defer final.Close()
		if err = unix.Fstat(int(final.Fd()), &want); err != nil {
			return pathError("stat", final.Name(), err)
		}
	default:
		return pathError("link", filepath.Join(dir, name), err)
	}
	if err = unix.Unlinkat(dfd, temp, 0); err != nil {
		return pathError("unlink", filepath.Join(dir, temp), err)
	}
	tempLinked = false
	if err = unix.Fsync(dfd); err != nil {
		return pathError("sync", dir, err)
	}
	var byName, byFD unix.Stat_t
	if err = unix.Fstatat(dfd, name, &byName, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return pathError("stat", filepath.Join(dir, name), err)
	}
	if err = unix.Fstat(int(final.Fd()), &byFD); err != nil {
		return pathError("stat", final.Name(), err)
	}
	if !same(byName, byFD) || !same(byFD, want) || byFD.Size != int64(len(raw)) || byFD.Mode != want.Mode || !retained(byFD, owner) {
		return ErrRefused
	}
	if err = directory(dir); err != nil {
		return err
	}
	var again unix.Stat_t
	if err = unix.Lstat(dir, &again); err != nil {
		return pathError("stat", dir, err)
	}
	if !same(again, held) {
		return ErrRefused
	}
	return nil
}

// exactRetry opens an existing final name and accepts it only if it is the same
// approval: a retained file with exactly raw. It returns the open file, fsynced.
func exactRetry(dfd int, dir, name string, raw []byte, limit int, owner uint32) (*os.File, error) {
	path := filepath.Join(dir, name)
	fd, err := unix.Openat(dfd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) {
		return nil, ErrRefused
	}
	if err != nil {
		return nil, pathError("open", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	keep := false
	defer func() {
		if !keep {
			f.Close()
		}
	}()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return nil, pathError("stat", path, err)
	}
	// Owner, mode and link count are checked by retained below.
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, ErrRefused
	}
	saved, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(saved, raw) {
		return nil, ErrRefused
	}
	if st.Nlink != 1 {
		if err = clearStaleTemps(dfd, dir, name, st); err != nil {
			return nil, err
		}
		if err = unix.Fstat(fd, &st); err != nil {
			return nil, pathError("stat", path, err)
		}
	}
	if !retained(st, owner) {
		return nil, ErrRefused
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	keep = true
	return f, nil
}

// clearStaleTemps unlinks this name's own temp files that are still links to the
// final file, left by a crash between link and unlink. Any other link remains
// and the caller refuses.
func clearStaleTemps(dfd int, dir, name string, final unix.Stat_t) error {
	lfd, err := unix.Openat(dfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return pathError("open", dir, err)
	}
	listing := os.NewFile(uintptr(lfd), dir)
	defer listing.Close()
	names, err := listing.Readdirnames(-1)
	if err != nil {
		return err
	}
	prefix := "." + name + tempMarker
	for _, n := range names {
		if !strings.HasPrefix(n, prefix) {
			continue
		}
		var st unix.Stat_t
		if err = unix.Fstatat(dfd, n, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return pathError("stat", filepath.Join(dir, n), err)
		}
		if same(st, final) {
			if err = unix.Unlinkat(dfd, n, 0); err != nil {
				return pathError("unlink", filepath.Join(dir, n), err)
			}
		}
	}
	if err = unix.Fsync(dfd); err != nil {
		return pathError("sync", dir, err)
	}
	return nil
}

func retained(st unix.Stat_t, owner uint32) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFREG && st.Uid == owner && st.Mode&07777 == 0600 && st.Nlink == 1
}

func same(a, b unix.Stat_t) bool { return a.Dev == b.Dev && a.Ino == b.Ino }

// validName accepts one plain path element that cannot collide with temp names.
func validName(name string) bool {
	return name != "" && len(name) <= maxName && name[0] != '.' && !strings.ContainsAny(name, "/\x00")
}

func tempName(name string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "." + name + tempMarker + hex.EncodeToString(random[:]), nil
}

func pathError(op, path string, err error) error { return &os.PathError{Op: op, Path: path, Err: err} }
