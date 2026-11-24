package remotessh

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

const (
	HandshakeTimeout    = 10 * time.Second
	MaxAuthTries        = 3
	MaxUnauthenticated  = 16
	MaxConnections      = 64
	MaxSessionsPerBench = 8
	SweepInterval       = time.Second
	ServerVersion       = "SSH-2.0-SuperBench"
)

// maxKeyOffers bounds certificate checks per connection: a client may offer
// keys and query them without signing, which MaxAuthTries does not count.
const maxKeyOffers = 2 * MaxAuthTries

// ErrConfig is a Config missing one of its parts.
var ErrConfig = errors.New("remotessh: incomplete configuration")

// panics counts recovered panics; the tests and fuzz targets require none.
var panics atomic.Uint64

func recovered(r any) bool {
	if r == nil {
		return false
	}
	panics.Add(1)
	return true
}

// errRefused is the one authentication error; its text never names a reason.
var errRefused = errors.New("refused")

type Config struct {
	Authority *remoteca.Authority
	Admit     func(remoteca.Claims) (remotestate.Admission, error)
	Launcher  Launcher
	Audit     Audit
	Registry  *Registry
	Now       func() time.Time
}

func (c Config) valid() error {
	if c.Authority == nil || c.Admit == nil || c.Launcher == nil || c.Audit == nil || c.Registry == nil {
		return ErrConfig
	}
	return nil
}

func (c Config) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

// admit re-checks the lease and requires the admission to name exactly the
// bench and claims the certificate carries.
func (c Config) admit(claims remoteca.Claims) (remotestate.Admission, error) {
	a, err := c.Admit(claims)
	if err != nil {
		return remotestate.Admission{}, err
	}
	if a.Claims != claims || a.BenchID != claims.BenchID || a.Root == "" {
		return remotestate.Admission{}, errRefused
	}
	return a, nil
}

// identity is what a verified certificate proved, carried from the
// authentication callbacks to the connection in Permissions.ExtraData.
type identity struct {
	claims      remoteca.Claims
	fingerprint string
	serial      uint64
	validBefore time.Time
	permitPTY   bool
}

type identityKey struct{}

// serverConfig is one connection's configuration. Public-key authentication
// with a certificate is the only method: no password, keyboard-interactive or
// NoClientAuth. The certificate is verified when offered; the lease is
// admitted only after the client proved it holds the key.
func serverConfig(c Config) *ssh.ServerConfig {
	offers := 0 // the callbacks run on the handshake goroutine only
	cfg := &ssh.ServerConfig{
		MaxAuthTries:            MaxAuthTries,
		ServerVersion:           ServerVersion,
		PublicKeyAuthAlgorithms: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256},
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if offers++; offers > maxKeyOffers {
				return nil, errRefused
			}
			claims, cert, err := c.Authority.Verify(meta.User(), key, c.now())
			if err != nil {
				return nil, errRefused
			}
			_, pty := cert.Extensions["permit-pty"]
			id := &identity{claims: claims, fingerprint: ssh.FingerprintSHA256(cert.Key), serial: cert.Serial, validBefore: time.Unix(int64(cert.ValidBefore), 0), permitPTY: pty}
			return &ssh.Permissions{ExtraData: map[any]any{identityKey{}: id}}, nil
		},
		VerifiedPublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey, p *ssh.Permissions, _ string) (*ssh.Permissions, error) {
			id, ok := p.ExtraData[identityKey{}].(*identity)
			if !ok {
				return nil, errRefused
			}
			if _, err := c.admit(id.claims); err != nil {
				return nil, errRefused
			}
			return p, nil
		},
	}
	cfg.AddHostKey(c.Authority.HostSigner())
	return cfg
}

// HandleConn serves one connection until it ends: the limits, the handshake
// under HandshakeTimeout, then the authenticated connection. It recovers a
// panic by closing the connection, so one connection never takes the
// process down.
func HandleConn(ctx context.Context, nc net.Conn, c Config) {
	defer func() {
		if recovered(recover()) {
			_ = nc.Close()
		}
	}()
	if c.valid() != nil || !c.Registry.admitConn() {
		_ = nc.Close()
		return
	}
	handshaking := true
	defer func() { c.Registry.dropConn(handshaking) }()
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	_ = nc.SetDeadline(time.Now().Add(HandshakeTimeout))
	sc, chans, reqs, err := ssh.NewServerConn(nc, serverConfig(c))
	if err != nil {
		_ = nc.Close()
		return
	}
	c.Registry.leaveHandshake()
	handshaking = false
	_ = nc.SetDeadline(time.Time{})
	id, ok := sc.Permissions.ExtraData[identityKey{}].(*identity)
	if !ok {
		_ = sc.Close()
		return
	}
	newConn(ctx, c, sc, id, nc.RemoteAddr().String()).serve(chans, reqs)
}

// Serve accepts connections on l until ctx ends or l fails, and sweeps every
// connection's lease each SweepInterval. On return every connection it
// served is closed and its sessions ended.
func Serve(ctx context.Context, l net.Listener, c Config) error {
	if err := c.valid(); err != nil {
		_ = l.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweepLoop(ctx, c)
	}()
	var err error
	var delay time.Duration
	for {
		nc, e := l.Accept()
		if e != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(e, net.ErrClosed) {
				err = e
				break
			}
			// A transient failure (no descriptors, for example): back off.
			delay = min(max(2*delay, 10*time.Millisecond), time.Second)
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			HandleConn(ctx, nc, c)
		}()
	}
	cancel()
	wg.Wait()
	return err
}

func sweepLoop(ctx context.Context, c Config) {
	t := time.NewTicker(SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep(c)
		}
	}
}

// sweep closes every connection whose deadline passed ("expired") or whose
// lease no longer admits its claims ("revoked"). Any refusal or error
// closes: the sweep fails closed, and a panic in it closes every connection.
func sweep(c Config) {
	conns := c.Registry.snapshot()
	defer func() {
		if recovered(recover()) {
			for _, x := range conns {
				x.close("revoked")
			}
		}
	}()
	checked := map[remoteca.Claims]error{}
	now := c.now()
	for _, x := range conns {
		if x.expiredAt(now) {
			x.close("expired")
			continue
		}
		err, ok := checked[x.claims]
		if !ok {
			_, err = c.admit(x.claims)
			checked[x.claims] = err
		}
		if err != nil {
			x.close("revoked")
		}
	}
}
