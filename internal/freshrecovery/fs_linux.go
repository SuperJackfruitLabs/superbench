//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retainedfile"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
	"golang.org/x/sys/unix"
)

var errAbsent = errors.New("fixed leaf is absent")

func nodeOf(st unix.Stat_t) Node {
	return Node{uint64(st.Dev), st.Ino, st.Mode, st.Uid, st.Gid}
}

// openBenches opens DataRoot/benches O_DIRECTORY|O_NOFOLLOW and requires the
// held directory to be the parent the selection pinned.
func openBenches(root string, pinned freshretirement.Parent) (*os.File, error) {
	name := filepath.Join(root, "benches")
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if (freshretirement.Parent{Device: uint64(st.Dev), Inode: st.Ino, Mode: uint32(info.Mode()), UID: st.Uid, GID: st.Gid}) != pinned {
		f.Close()
		return nil, ErrRecovery
	}
	return f, nil
}

// held is an observed leaf with every present fixed directory held open.
type held struct {
	Leaf
	dirs map[string]*os.File // by relative name; "." is the leaf
}

func (h *held) close() {
	for _, f := range h.dirs {
		f.Close()
	}
}

func openDir(parent *os.File, name, label string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return nil, st, ErrRecovery
	}
	if err != nil {
		return nil, st, &os.PathError{Op: "openat", Path: label, Err: err}
	}
	f := os.NewFile(uintptr(fd), label)
	var named unix.Stat_t
	if err = unix.Fstat(fd, &st); err == nil {
		err = unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW)
	}
	if err != nil || st.Dev != named.Dev || st.Ino != named.Ino {
		f.Close()
		return nil, st, errors.Join(ErrRecovery, err)
	}
	return f, st, nil
}

// observe opens the fixed leaf below the held benches parent and the prefix of
// fixed directories present beneath it. It requires every held directory to
// contain exactly its present fixed children, with a matching link count, so
// the leaf holds nothing but empty fixed directories. ENOENT returns errAbsent.
func observe(benches *os.File, key string, fixed []string) (*held, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(int(benches.Fd()), key, &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil, errAbsent
	} else if err != nil {
		return nil, err
	}
	leaf, st, err := openDir(benches, key, filepath.Join(benches.Name(), key))
	if err != nil {
		return nil, err
	}
	h := &held{Leaf: Leaf{Node: nodeOf(st)}, dirs: map[string]*os.File{".": leaf}}
	if h.ProjectID, err = projectID(leaf); err == nil {
		err = h.walk(fixed)
	}
	if err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}

// walk holds the prefix of fixed directories present below the held leaf, in
// creation order, and requires each held directory to contain exactly its
// present fixed children. Begin creates parents first, so the first missing
// name ends the prefix and any later one would be an unexpected entry.
func (h *held) walk(fixed []string) error {
	leaf := h.dirs["."]
	for _, name := range fixed {
		parent, ok := h.dirs[path.Dir(name)]
		if !ok {
			break
		}
		var st unix.Stat_t
		err := unix.Fstatat(int(parent.Fd()), path.Base(name), &st, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			break
		}
		var dir *os.File
		if err == nil {
			dir, st, err = openDir(parent, path.Base(name), filepath.Join(leaf.Name(), name))
		}
		if err != nil {
			return err
		}
		h.dirs[name] = dir
		h.Fixed = append(h.Fixed, Entry{name, nodeOf(st)})
	}
	present := make([]string, len(h.Fixed))
	for k, e := range h.Fixed {
		present[k] = e.Name
	}
	for name, want := range children(present) {
		if err := exactly(h.dirs[name], want); err != nil {
			return err
		}
	}
	return nil
}

// exactly requires dir to hold only the named subdirectories: no other entry,
// and a link count of two plus those subdirectories.
func exactly(dir *os.File, want []string) error {
	if _, err := dir.Seek(0, io.SeekStart); err != nil {
		return err
	}
	names, err := dir.Readdirnames(len(want) + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var st unix.Stat_t
	if err = unix.Fstat(int(dir.Fd()), &st); err != nil {
		return err
	}
	slices.Sort(names)
	sorted := slices.Sorted(slices.Values(want))
	if !slices.Equal(names, sorted) || st.Nlink != uint64(2+len(want)) {
		return ErrRecovery
	}
	return nil
}

// rmdirAt removes name below parent only while it is still the held, empty
// directory want, then requires the held fd to report it unlinked and fsyncs
// the parent so the removal is committed.
func rmdirAt(parent, self *os.File, name string, want Node) error {
	var named, st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if err := unix.Fstat(int(self.Fd()), &st); err != nil {
		return err
	}
	if nodeOf(named) != want || nodeOf(st) != want {
		return ErrRecovery
	}
	if err := exactly(self, nil); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return &os.PathError{Op: "rmdir", Path: self.Name(), Err: err}
	}
	if err := unix.Fstat(int(self.Fd()), &st); err != nil {
		return err
	}
	if st.Nlink != 0 {
		return ErrRecovery
	}
	return unix.Fsync(int(parent.Fd()))
}

// projectID reads the XFS project ID through the held fd, not by path.
func projectID(f *os.File) (uint32, error) {
	var a fsxattr
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), fsIocFsgetxattr, uintptr(unsafe.Pointer(&a))); e != 0 {
		return 0, &os.PathError{Op: "FS_IOC_FSGETXATTR", Path: f.Name(), Err: e}
	}
	return a.Projid, nil
}

// retain publishes the reviewed plan as LEDGER/payload-cleanups/KEY.STEP.json
// before the first effect. When existing is set the step is a retry after the
// effect, so the approval must already exist; it is never created afresh then.
func retain(ledger, key, step string, raw []byte, existing bool) error {
	return retainFile(ledger, key+"."+step+".json", raw, existing, MaxPlan)
}

// retainFile publishes raw exactly once as LEDGER/payload-cleanups/name, up to
// limit bytes, with the same existing-approval rule as retain.
func retainFile(ledger, name string, raw []byte, existing bool, limit int) error {
	if existing {
		// The leaf is already gone: without this step's approval it was not ours.
		if _, err := os.Lstat(filepath.Join(ledger, "payload-cleanups", name)); err != nil {
			return errors.Join(ErrRecovery, err)
		}
	}
	parent, err := cleanups(ledger)
	if err != nil {
		return err
	}
	if err = retainedfile.Publish(parent, name, raw, limit); errors.Is(err, retainedfile.ErrRefused) {
		return errors.Join(ErrRecovery, err)
	}
	return err
}

// cleanups returns LEDGER/payload-cleanups, created root-owned and private
// beneath the root-owned ledger, with the ledger fsynced.
func cleanups(ledger string) (string, error) {
	parent := filepath.Join(ledger, "payload-cleanups")
	if err := storagecustody.RootOwnedDirectory(ledger, true); err != nil {
		return "", err
	}
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if err := storagecustody.RootOwnedDirectory(parent, true); err != nil {
		return "", err
	}
	d, err := os.Open(ledger)
	if err != nil {
		return "", err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return "", err
	}
	return parent, nil
}

// bounded keeps at most 64 KiB of a fixed tool's output.
type bounded struct{ raw []byte }

func (b *bounded) Write(raw []byte) (int, error) {
	if len(b.raw)+len(raw) > 64<<10 {
		return 0, ErrRecovery
	}
	b.raw = append(b.raw, raw...)
	return len(raw), nil
}

// tool runs a fixed binary with a fixed environment and returns its bounded
// standard output and standard error.
func tool(ctx context.Context, binary string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	var out, diagnostic bounded
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w: %s", path.Base(binary), err, strings.TrimSpace(string(diagnostic.raw)))
	}
	return out.raw, diagnostic.raw, nil
}
