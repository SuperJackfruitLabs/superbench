//go:build linux

package freshliveness

import (
	"os"
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

// Installed reads every cgroup beneath cgroupRoot, nested ones too, and
// /proc. A process uses the bench when any of its tasks has a working
// directory, root, open file or file mapping on the data device in the
// bench's project. Root opens such an entry only through /proc to read its
// project; an entry on the data device it cannot attribute counts as use, and
// so does a process with live tasks it cannot read. This is a best-effort,
// point-in-time check: the empty-leaf removal and the zero-usage check at
// retirement are the guarantees, and the project attribution is only as good
// as the payload's inability to relabel quota projects.
func Installed(cgroupRoot string, payloadUID uint32, dataDevice uint64, project uint32) Liveness {
	return adapter{cgroupRoot, "/proc", os.Getpid(), payloadUID, dataDevice, project, projectID}.liveness()
}

// projectID reads the XFS project ID through the held descriptor.
func projectID(fd int) (uint32, error) {
	var x fsxattr
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsIocFsgetxattr, uintptr(unsafe.Pointer(&x))); e != 0 {
		return 0, e
	}
	return x.Projid, nil
}
