//go:build linux

package gitcontrol

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// Quiescence retains a read-only empty-inventory observation, never a process
// fence or ownership grant. The caller must retain the real host shared lock
// across this observation, its protected work and Recheck. Every pending or
// unknown entry blocks observation, including entries for unrelated operations.
type Quiescence struct {
	mu       sync.Mutex
	state    string
	uid, gid uint32
	files    []*os.File
	initial  []os.FileInfo
	journal  bool
	closed   bool
}

// Quiescent never creates a directory, lock, intent or cgroup. It uses stronger
// custody checks than legacy controlDir: actual daemon ownership of private
// state/journal directories, physical pinned ancestry and empty inventory.
func Quiescent(state string) (*Quiescence, error) {
	q, err := openQuiescent(state)
	if err != nil {
		return nil, err
	}
	if err = q.Recheck(); err != nil {
		_ = q.Close()
		return nil, err
	}
	return q, nil
}

func openQuiescent(state string) (_ *Quiescence, err error) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if uid == 0 || gid == 0 || os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() || !utf8.ValidString(state) || strings.ContainsRune(state, 0) || len(state) > 4096 || !filepath.IsAbs(state) || filepath.Clean(state) != state || state == "/" {
		return nil, quietRefusal(nil)
	}
	q := &Quiescence{state: state, uid: uid, gid: gid}
	defer func() {
		if err != nil {
			_ = q.Close()
		}
	}()
	parent, e := os.Open("/")
	if e != nil {
		return nil, quietRefusal(e)
	}
	if e = q.retain(parent, false); e != nil {
		return nil, e
	}
	for _, name := range strings.Split(strings.TrimPrefix(state, "/"), "/") {
		fd, e := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if e != nil {
			return nil, quietRefusal(e)
		}
		parent = os.NewFile(uintptr(fd), "Git control state ancestor")
		if e = q.retain(parent, false); e != nil {
			return nil, e
		}
	}
	if e = q.private(q.initial[len(q.initial)-1]); e != nil {
		return nil, e
	}
	fd, e := syscall.Openat(int(parent.Fd()), "git-controls", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if e != nil {
		if e == syscall.ENOENT {
			return q, nil
		}
		return nil, quietRefusal(e)
	}
	journal := os.NewFile(uintptr(fd), "Git control inventory")
	if e = q.retain(journal, true); e != nil {
		return nil, e
	}
	q.journal = true
	entries, e := journal.ReadDir(1)
	if len(entries) != 0 || e != io.EOF {
		return nil, quietRefusal(e)
	}
	// Retain the metadata from after the empty read. Later create/remove activity
	// must not become an apparently unchanged empty inventory.
	after, e := journal.Stat()
	if e != nil {
		return nil, quietRefusal(e)
	}
	if !quietSame(q.initial[len(q.initial)-1], after, true) {
		return nil, quietRefusal(nil)
	}
	return q, nil
}

func (q *Quiescence) retain(f *os.File, private bool) error {
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return quietRefusal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st.Uid != 0 && (st.Uid != q.uid || st.Gid != q.gid) || info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) || private && q.private(info) != nil {
		_ = f.Close()
		return quietRefusal(nil)
	}
	q.files = append(q.files, f)
	q.initial = append(q.initial, info)
	return nil
}
func (q *Quiescence) private(info os.FileInfo) error {
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st.Uid != q.uid || st.Gid != q.gid || info.Mode() != os.ModeDir|0700 {
		return quietRefusal(nil)
	}
	return nil
}
func quietSame(a, b os.FileInfo, history bool) bool {
	x, y := a.Sys().(*syscall.Stat_t), b.Sys().(*syscall.Stat_t)
	return os.SameFile(a, b) && a.Mode() == b.Mode() && x.Uid == y.Uid && x.Gid == y.Gid && (!history || x.Mtim == y.Mtim && x.Ctim == y.Ctim)
}

// Recheck compares the original handles to the current named ancestry and
// journal. An absent journal cannot silently become an adopted empty directory.
func (q *Quiescence) Recheck() error {
	if q == nil {
		return quietRefusal(nil)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.state == "" || uint32(os.Geteuid()) != q.uid || uint32(os.Getegid()) != q.gid {
		return quietRefusal(nil)
	}
	current, err := openQuiescent(q.state)
	if err != nil {
		return err
	}
	defer current.Close()
	if q.journal != current.journal || len(q.files) != len(current.files) {
		return quietRefusal(nil)
	}
	for i, f := range q.files {
		before := q.initial[i]
		now, e := f.Stat()
		named, ne := current.files[i].Stat()
		// The final state directory also tracks history, catching creation/removal
		// of a previously absent journal during the protected interval.
		history := i >= len(q.files)-1 || q.journal && i == len(q.files)-2
		if e != nil {
			return quietRefusal(e)
		}
		if ne != nil {
			return quietRefusal(ne)
		}
		if !quietSame(before, now, history) || !quietSame(before, named, history) {
			return quietRefusal(nil)
		}
	}
	return nil
}
func (q *Quiescence) Close() error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	var first error
	for _, f := range q.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	q.files = nil
	q.initial = nil
	if first != nil {
		return quietRefusal(first)
	}
	return nil
}
