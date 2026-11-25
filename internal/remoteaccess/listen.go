package remoteaccess

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

// errWildcard is a fleet address that would listen on every interface.
// Activation refuses it first; the listener refuses it again.
var errWildcard = errors.New("remote access listens only on one exact fleet address, never a wildcard or zoned address")

// listenFleet binds exactly addr. Where the kernel allows it (Linux:
// IP_FREEBIND, IPV6_FREEBIND) the bind succeeds before any interface carries
// the address, so benchd can start before the tailnet brings it up and
// accepts connections from the moment it does. Freebind changes only when the
// address must exist: the listener is still bound to that one address, and
// any other bind error, such as an address already in use, is returned.
func listenFleet(ctx context.Context, addr netip.AddrPort) (net.Listener, error) {
	if !addr.IsValid() || addr.Addr().Unmap().IsUnspecified() || addr.Addr().Zone() != "" {
		return nil, errWildcard
	}
	lc := net.ListenConfig{Control: freebind}
	return lc.Listen(ctx, "tcp", addr.String())
}
