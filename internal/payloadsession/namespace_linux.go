//go:build linux

package payloadsession

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"unsafe"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
)

// The shipped architectures share these numbers; others refuse in Enter.
const (
	sysOpenTree         = 428
	sysMoveMount        = 429
	openTreeClone       = 1
	atEmptyPath         = 0x1000
	moveMountFEmptyPath = 0x4
	tmpfsMagic          = 0x01021994
	stRdonly            = 1
)

var benchKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

// Enter locks the calling goroutine to its thread for the rest of the process
// and gives that thread a private mount namespace in which DataRoot and
// StateRoot are read-only tmpfs and the only bench is bench, at its own path.
// The caller must exec from this goroutine. bench is the pinned, verified
// workspace root; no step resolves a name an unprivileged user can change.
func Enter(p payloadpolicy.Policy, key string, bench *os.File) error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" || p.Validate() != nil || !benchKey.MatchString(key) || bench == nil {
		return ErrSession
	}
	var pinned syscall.Stat_t
	if err := syscall.Fstat(int(bench.Fd()), &pinned); err != nil || pinned.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return errors.Join(ErrSession, err)
	}
	runtime.LockOSThread() // Never unlocked: mount namespaces are per thread.
	// Clone first: older kernels refuse to clone a mount of another namespace,
	// and bench was pinned in the original one.
	empty := []byte{0}
	clone, _, errno := syscall.Syscall(sysOpenTree, bench.Fd(), uintptr(unsafe.Pointer(&empty[0])), openTreeClone|syscall.O_CLOEXEC|atEmptyPath)
	if errno != 0 {
		return errors.Join(ErrSession, errno)
	}
	defer syscall.Close(int(clone))
	if err := syscall.Unshare(syscall.CLONE_NEWNS); err != nil {
		return errors.Join(ErrSession, err)
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return errors.Join(ErrSession, err)
	}
	var roots []*os.File
	defer func() {
		for _, root := range roots {
			_ = root.Close()
		}
	}()
	for _, path := range []string{p.DataRoot, p.StateRoot} {
		root, err := tmpfs(path, p.DaemonUID)
		if err != nil {
			return err
		}
		roots = append(roots, root)
	}
	data := int(roots[0].Fd())
	if err := syscall.Mkdirat(data, "benches", 0755); err != nil {
		return errors.Join(ErrSession, err)
	}
	benches, err := openDirectory(data, "benches")
	if err != nil {
		return err
	}
	defer benches.Close()
	if err := syscall.Mkdirat(int(benches.Fd()), key, 0755); err != nil {
		return errors.Join(ErrSession, err)
	}
	target := append([]byte(key), 0)
	if _, _, errno := syscall.Syscall6(sysMoveMount, clone, uintptr(unsafe.Pointer(&empty[0])), benches.Fd(), uintptr(unsafe.Pointer(&target[0])), moveMountFEmptyPath, 0); errno != 0 {
		return errors.Join(ErrSession, errno)
	}
	mounted, err := openDirectory(int(benches.Fd()), key)
	if err != nil {
		return err
	}
	defer mounted.Close()
	if !same(mounted, pinned) {
		return ErrSession
	}
	// The clone is a peer of the host mount it came from; cut propagation.
	if err := syscall.Mount("", fdPath(mounted), "", syscall.MS_PRIVATE, ""); err != nil {
		return errors.Join(ErrSession, err)
	}
	if err := syscall.Mount("", fdPath(mounted), "", syscall.MS_REMOUNT|syscall.MS_BIND|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		return errors.Join(ErrSession, err)
	}
	for _, root := range roots {
		if err := syscall.Mount("", fdPath(root), "", syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
			return errors.Join(ErrSession, err)
		}
	}
	return verify(p, key, pinned, roots)
}

// tmpfs mounts a fresh tmpfs over the verified physical directory at path,
// through its descriptor, and returns the new tmpfs root found again by path.
func tmpfs(path string, daemonUID uint32) (*os.File, error) {
	under, err := registeredinspection.OpenPhysicalDirectory(path, daemonUID)
	if err != nil {
		return nil, errors.Join(ErrSession, err)
	}
	var below syscall.Stat_t
	err = syscall.Fstat(int(under.Fd()), &below)
	if err == nil {
		err = syscall.Mount("tmpfs", fdPath(under), "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "mode=0755,size=64k,nr_inodes=64")
	}
	_ = under.Close()
	if err != nil {
		return nil, errors.Join(ErrSession, err)
	}
	root, err := registeredinspection.OpenPhysicalDirectory(path, daemonUID)
	if err != nil {
		return nil, errors.Join(ErrSession, err)
	}
	var st syscall.Stat_t
	var fs syscall.Statfs_t
	if syscall.Fstat(int(root.Fd()), &st) != nil || syscall.Fstatfs(int(root.Fd()), &fs) != nil || fs.Type != tmpfsMagic || st.Dev == below.Dev || st.Uid != 0 || st.Mode&07777 != 0755 {
		_ = root.Close()
		return nil, ErrSession
	}
	return root, nil
}

// verify: by descriptor and again by name, DataRoot holds only benches, which
// holds only key, which is the pinned bench; StateRoot is empty; both are
// read-only. The session's chdir resolves this same path.
func verify(p payloadpolicy.Policy, key string, pinned syscall.Stat_t, roots []*os.File) error {
	want := map[string][]string{p.DataRoot: {"benches"}, filepath.Join(p.DataRoot, "benches"): {key}, p.StateRoot: nil}
	for path, names := range want {
		directory, err := registeredinspection.OpenPhysicalDirectory(path, p.DaemonUID)
		if err != nil {
			return errors.Join(ErrSession, err)
		}
		found, err := directory.Readdirnames(-1)
		_ = directory.Close()
		if err != nil || !slices.Equal(found, names) {
			return errors.Join(ErrSession, err)
		}
	}
	for _, root := range roots {
		var fs syscall.Statfs_t
		if err := syscall.Fstatfs(int(root.Fd()), &fs); err != nil || fs.Flags&stRdonly == 0 {
			return errors.Join(ErrSession, err)
		}
	}
	var named syscall.Stat_t
	if err := syscall.Stat(filepath.Join(p.DataRoot, "benches", key), &named); err != nil || named.Dev != pinned.Dev || named.Ino != pinned.Ino {
		return errors.Join(ErrSession, err)
	}
	return nil
}

func openDirectory(parent int, name string) (*os.File, error) {
	fd, err := syscall.Openat(parent, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrSession, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func same(f *os.File, pinned syscall.Stat_t) bool {
	var st syscall.Stat_t
	return syscall.Fstat(int(f.Fd()), &st) == nil && st.Dev == pinned.Dev && st.Ino == pinned.Ino
}

func fdPath(f *os.File) string { return "/proc/self/fd/" + strconv.Itoa(int(f.Fd())) }
