//go:build linux

package cleanworker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

const prSetNoNewPrivs = 38

// Main runs the worker: args are the two IDs after the command. It returns the
// process exit code; a refusal is written to stderr and nothing to stdout.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := run(args, stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "bench-quota clean-worker:", err)
		return 1
	}
	return 0
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	// Before anything else: no_new_privs, then the descriptor table (read with
	// raw syscalls, before any os.File can open the runtime's poller), then the
	// credentials, then refuse root.
	if err := noNewPrivs(); err != nil {
		return err
	}
	want, err := parseArgs(args)
	if err != nil {
		return err
	}
	if err = checkDescriptors(); err != nil {
		return err
	}
	if err = checkCredentials(want); err != nil {
		return err
	}
	if err = reap.RequireNonRoot(); err != nil || os.Getuid() == 0 {
		return errors.Join(ErrWorker, err)
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, MaxFrame+1))
	if err != nil {
		return err
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		return err
	}
	reply := Reply{Version: Version}
	switch request.Mode {
	case Observe:
		if reply, err = observe(request.Root); err != nil {
			return err
		}
	case Delete:
		// DeleteContents re-inspects each entry beneath an os.Root, deletes in
		// reverse order and leaves the root. Remaining accepts approved entries
		// that are already gone and refuses anything changed or new.
		if err = reap.DeleteContents(request.Root, *request.Approved); err != nil {
			return err
		}
	}
	out, err := EncodeReply(reply, request.Mode)
	if err != nil {
		return err
	}
	_, err = stdout.Write(out)
	return err
}

// noNewPrivs sets no_new_privs on every thread. A cgo build cannot use
// AllThreadsSyscall; it falls back to the calling thread. Root also sets it on
// the thread that forks the worker, so the whole worker inherits it, and the
// status check below proves it either way.
func noNewPrivs() error {
	_, _, errno := syscall.AllThreadsSyscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0)
	if errno == syscall.ENOTSUP {
		_, _, errno = syscall.RawSyscall(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0)
	}
	if errno != 0 {
		return fmt.Errorf("%w: no_new_privs: %v", ErrWorker, errno)
	}
	return nil
}

// checkDescriptors lists /proc/self/fd with raw syscalls, so the listing is not
// polluted by the runtime's poller descriptors, and requires only 0, 1 and 2.
func checkDescriptors() error {
	fd, err := syscall.Open("/proc/self/fd", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: descriptors: %v", ErrWorker, err)
	}
	defer syscall.Close(fd)
	var names []string
	buf := make([]byte, 8192)
	for {
		n, err := syscall.Getdents(fd, buf)
		if err != nil {
			return fmt.Errorf("%w: descriptors: %v", ErrWorker, err)
		}
		if n <= 0 {
			break
		}
		if _, _, names = syscall.ParseDirent(buf[:n], -1, names); len(names) > 1024 {
			return fmt.Errorf("%w: descriptors", ErrWorker)
		}
	}
	return verifyDescriptors(names, fd)
}

// checkCredentials requires the real, effective and saved IDs to be exactly
// want, with no supplementary groups and no capabilities, through both the
// system calls and /proc/self/status.
func checkCredentials(want Identity) error {
	if uint32(os.Getuid()) != want.UID || uint32(os.Geteuid()) != want.UID || uint32(os.Getgid()) != want.GID || uint32(os.Getegid()) != want.GID {
		return fmt.Errorf("%w: Uid", ErrWorker)
	}
	groups, err := syscall.Getgroups()
	if err != nil || len(groups) != 0 {
		return fmt.Errorf("%w: Groups", ErrWorker)
	}
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return errors.Join(ErrWorker, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return errors.Join(ErrWorker, err)
	}
	return verifyStatus(raw, want)
}

// observe snapshots the tree, then Lstats every entry relative to a held
// os.Root and requires the same node and mode the snapshot hashed.
func observe(root string) (Reply, error) {
	tree, err := reap.Snapshot(root, nil)
	if err != nil {
		return Reply{}, err
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return Reply{}, err
	}
	defer handle.Close()
	entries := append([]reap.Entry{tree.Root}, tree.Entries...)
	owners := make([]Owner, 0, len(entries))
	for _, e := range entries {
		info, err := handle.Lstat(e.Path)
		if err != nil {
			return Reply{}, err
		}
		st := info.Sys().(*syscall.Stat_t)
		if uint64(st.Dev) != e.Device || st.Ino != e.Inode || uint32(info.Mode()) != e.Mode {
			return Reply{}, fmt.Errorf("%w: %s changed during observation", ErrWorker, e.Path)
		}
		owners = append(owners, Owner{Path: e.Path, Device: uint64(st.Dev), Inode: st.Ino, Nlink: uint64(st.Nlink), Mode: uint32(info.Mode()), UID: st.Uid, GID: st.Gid})
	}
	named, err := os.Lstat(root)
	if err != nil {
		return Reply{}, err
	}
	if st := named.Sys().(*syscall.Stat_t); uint64(st.Dev) != tree.Root.Device || st.Ino != tree.Root.Inode {
		return Reply{}, fmt.Errorf("%w: root changed during observation", ErrWorker)
	}
	return Reply{Version: Version, Tree: &tree, Owners: owners}, nil
}
