//go:build linux

package freshliveness

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fsIocFsgetxattr is _IOR('X', 31, struct fsxattr) on amd64 and arm64.
const fsIocFsgetxattr = uintptr(2<<30 | 28<<16 | 'X'<<8 | 31)

// fsxattr mirrors the kernel's struct fsxattr.
type fsxattr struct {
	Xflags, Extsize, Nextents, Projid, Cowextsize uint32
	Pad                                           [8]byte
}

// Installed reads the cgroup scopes beneath cgroupRoot and /proc. A
// process uses the bench when its working directory, root or an open regular
// file or directory is on the data device and carries the bench's project;
// root opens such an entry only through /proc to read that project.
func Installed(cgroupRoot string, payloadUID uint32, dataDevice uint64, project uint32) Liveness {
	self := os.Getpid()
	read := func(path string, limit int64) ([]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, limit))
	}
	gone := func(err error) bool { return errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) }
	return Liveness{
		Scopes: func() ([]string, error) {
			entries, err := os.ReadDir(cgroupRoot)
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			var names []string
			for _, e := range entries {
				if e.IsDir() {
					names = append(names, e.Name())
				}
			}
			return names, nil
		},
		Events: func(scope string) ([]byte, error) {
			raw, err := read(filepath.Join(cgroupRoot, scope, "cgroup.events"), 4096)
			if gone(err) {
				return []byte("populated 0\n"), nil
			}
			return raw, err
		},
		Members: func(scope string) ([]int, error) {
			raw, err := os.ReadFile(filepath.Join(cgroupRoot, scope, "cgroup.procs"))
			if gone(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			var pids []int
			for _, f := range strings.Fields(string(raw)) {
				pid, err := strconv.Atoi(f)
				if err != nil {
					return nil, ErrBusy
				}
				pids = append(pids, pid)
			}
			return pids, nil
		},
		Processes: func() ([]int, error) {
			entries, err := os.ReadDir("/proc")
			if err != nil {
				return nil, err
			}
			var pids []int
			for _, e := range entries {
				if pid, err := strconv.Atoi(e.Name()); err == nil && pid != self {
					pids = append(pids, pid)
				}
			}
			return pids, nil
		},
		Payload: func(pid int) (bool, error) {
			raw, err := read("/proc/"+strconv.Itoa(pid)+"/status", 64<<10)
			if gone(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			want := strconv.FormatUint(uint64(payloadUID), 10)
			for _, line := range strings.Split(string(raw), "\n") {
				if key, value, ok := strings.Cut(line, ":"); ok && key == "Uid" {
					for _, id := range strings.Fields(value) {
						if id == want {
							return true, nil
						}
					}
				}
			}
			return false, nil
		},
		Uses: func(pid int) (bool, error) {
			if pid == self {
				return false, nil
			}
			base := "/proc/" + strconv.Itoa(pid)
			paths := []string{base + "/cwd", base + "/root"}
			entries, err := os.ReadDir(base + "/fd")
			if gone(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			for _, e := range entries {
				paths = append(paths, base+"/fd/"+e.Name())
			}
			for _, path := range paths {
				var st unix.Stat_t
				if unix.Stat(path, &st) != nil || st.Dev != dataDevice {
					continue
				}
				if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFREG && kind != unix.S_IFDIR {
					continue
				}
				fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
				if gone(err) {
					continue
				}
				if err != nil {
					// An entry on the data volume that cannot be attributed counts as use.
					return true, nil
				}
				var a fsxattr
				_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsIocFsgetxattr, uintptr(unsafe.Pointer(&a)))
				unix.Close(fd)
				if e != 0 || a.Projid == project {
					return true, nil
				}
			}
			return false, nil
		},
	}
}
