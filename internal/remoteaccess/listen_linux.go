package remoteaccess

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// freebind sets IP_FREEBIND or IPV6_FREEBIND on the fleet socket before bind.
// Neither needs a capability.
func freebind(network, _ string, c syscall.RawConn) error {
	var set error
	err := c.Control(func(fd uintptr) {
		switch network {
		case "tcp4":
			set = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_FREEBIND, 1)
		case "tcp6":
			set = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_FREEBIND, 1)
		default:
			set = fmt.Errorf("fleet listener: unexpected network %q", network)
		}
	})
	if set != nil {
		set = fmt.Errorf("fleet listener: freebind: %w", set)
	}
	return errors.Join(err, set)
}
