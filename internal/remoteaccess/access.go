// Package remoteaccess composes benchd's remote access: the CA and host key,
// the fleet SSH listener and the /v2/remote/* socket routes. It exists only
// when activation enables remote_access; New opens nothing, and the CA, the
// host key and the listener are opened by Serve alone.
package remoteaccess

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteapi"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotesession"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

var (
	// ErrOff is a New without remote access fully enabled.
	ErrOff = errors.New("remote access is not enabled")
	// ErrServing is a second Serve on the same Access.
	ErrServing = errors.New("remote access is already serving")
)

// Access satisfies service.Remote. Its authority and state manager exist only
// while Serve runs; until then, and after it returns, the routes answer 503.
type Access struct {
	store              bench.Store
	address            netip.AddrPort
	operators, readers map[uint32]bool
	registry           *remotessh.Registry
	launcher           remotessh.Launcher
	scopes             remotestate.Scopes // nil means the host's cgroups
	listener           net.Listener       // tests only: a pre-bound listener
	now                func() time.Time   // tests only: the certificate clock
	host               *host              // tests only: nil means the installed host

	mu        sync.Mutex
	serving   bool
	authority *remoteca.Authority
	state     remotestate.Manager
}

// New binds remote access to the store and the fleet address. It refuses
// unless activation enables remote_access with fresh_payload and an address,
// and it opens no file, key or socket.
func New(store bench.Store, s activation.Switches, operators, readers map[uint32]bool) (*Access, error) {
	switch {
	case !s.RemoteAccess:
		return nil, ErrOff
	case !s.FreshPayload:
		return nil, errors.Join(ErrOff, errors.New("remote_access requires fresh_payload"))
	case !s.RemoteFleetAddress.IsValid():
		return nil, errors.Join(ErrOff, errors.New("remote_access requires remote_fleet_address"))
	}
	return &Access{store: store, address: s.RemoteFleetAddress, operators: operators, readers: readers, registry: remotessh.NewRegistry(), launcher: remotesession.Installed{}}, nil
}

// Serve opens the CA and host key under <state>/remote, listens on the fleet
// address and serves SSH until ctx ends. It refuses, before it opens
// anything, a host that misses a deploy requirement (ErrHost), and it
// refuses state no earlier start migrated. It returns only after every
// connection it served has closed.
func (a *Access) Serve(ctx context.Context) error {
	a.mu.Lock()
	if a.serving {
		a.mu.Unlock()
		return ErrServing
	}
	a.serving = true
	a.mu.Unlock()
	l := a.listener
	closeListener := func() {
		if l != nil {
			_ = l.Close()
		}
	}
	h := installedHost
	if a.host != nil {
		h = *a.host
	}
	if err := h.check(a.store); err != nil {
		closeListener()
		return err
	}
	if err := a.store.RequireOperationState(); err != nil {
		closeListener()
		return err
	}
	authority, err := remoteca.Open(filepath.Join(a.store.State, "remote"))
	if err != nil {
		closeListener()
		return err
	}
	state, err := remotestate.ForStore(a.store, a.scopes)
	if err != nil {
		closeListener()
		return err
	}
	if l == nil {
		if l, err = listenFleet(ctx, a.address); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.authority, a.state = authority, state
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.authority, a.state = nil, remotestate.Manager{}
		a.mu.Unlock()
	}()
	log.Printf("remote access: listening on %s, host key %s", l.Addr(), authority.HostFingerprint())
	return remotessh.Serve(ctx, l, remotessh.Config{Authority: authority, Admit: admit(state), Launcher: a.launcher, Audit: state, Registry: a.registry})
}

// admit is the lease check of every connection, channel and sweep: the
// claims must name the live lease and remote execution, and that
// execution's scope must still exist. A scope a benchd restart or a reboot
// removed refuses its certificates before any session reaches the launcher.
func admit(m remotestate.Manager) func(remoteca.Claims) (remotestate.Admission, error) {
	return func(c remoteca.Claims) (remotestate.Admission, error) {
		a, err := m.Admit(c)
		if err != nil {
			return remotestate.Admission{}, err
		}
		present, err := m.Scopes.Present(&a.Execution)
		if err != nil {
			return remotestate.Admission{}, errors.Join(remotestate.ErrUnavailable, err)
		}
		if !present {
			return remotestate.Admission{}, remotestate.ErrRevoked
		}
		return a, nil
	}
}

// Route serves /v2/remote/*. Before Serve has opened the authority, and
// after it returns, every authorized request is 503.
func (a *Access) Route(w http.ResponseWriter, r *http.Request, uid uint32, admit func(*http.Request) error) {
	a.mu.Lock()
	h := remoteapi.Handler{Address: a.address.String(), Operators: a.operators, Readers: a.readers, Extend: a.registry.Extend, Now: a.now}
	if a.authority != nil {
		state := a.state
		h.Authority, h.State = a.authority, func() (remotestate.Manager, error) { return state, nil }
	}
	a.mu.Unlock()
	h.Serve(w, r, uid, admit)
}
