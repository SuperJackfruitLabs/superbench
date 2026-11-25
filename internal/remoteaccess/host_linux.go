package remoteaccess

import (
	"errors"
	"runtime"
	"syscall"
	"unsafe"
)

// sysMountSetattr is mount_setattr on amd64 and arm64, the shipped
// architectures; payloadsession.Enter uses the same number.
const sysMountSetattr = 442

// probeMountSetattr calls mount_setattr on a descriptor that cannot exist.
// A kernel with the call refuses the descriptor (EBADF), or a filter refuses
// the call (EPERM); only ENOSYS means the kernel lacks it.
func probeMountSetattr() error {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return errors.New("unsupported architecture " + runtime.GOARCH)
	}
	empty := []byte{0}
	var attr [4]uint64 // struct mount_attr
	badFD := -1
	_, _, errno := syscall.Syscall6(sysMountSetattr, uintptr(badFD), uintptr(unsafe.Pointer(&empty[0])), 0x1000, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno == syscall.ENOSYS {
		return errno
	}
	return nil
}
