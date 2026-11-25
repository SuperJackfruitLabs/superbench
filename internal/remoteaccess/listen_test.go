package remoteaccess

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

// Freebind changes only when the address must exist: a wildcard or zoned
// fleet address is still refused before any socket is opened.
func TestFleetListenerRefusesWildcards(t *testing.T) {
	for _, text := range []string{"0.0.0.0:0", "[::]:0", "[::ffff:0.0.0.0]:0", "[fe80::1%lo]:0"} {
		l, err := listenFleet(context.Background(), netip.MustParseAddrPort(text))
		if l != nil {
			l.Close()
		}
		if !errors.Is(err, errWildcard) {
			t.Fatalf("%s: listenFleet %v", text, err)
		}
	}
	if l, err := listenFleet(context.Background(), netip.AddrPort{}); l != nil || !errors.Is(err, errWildcard) {
		t.Fatal("an unset address bound", err)
	}
}
