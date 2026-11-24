package remotessh

import (
	"context"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
)

// conn is one authenticated connection. Its identity fields never change
// after newConn; mu guards the deadline, the close reason and the sessions.
type conn struct {
	id          uint64
	cfg         Config
	sc          *ssh.ServerConn
	parent      context.Context
	ctx         context.Context // ends when the connection closes; bounds Launcher.Start
	cancel      context.CancelFunc
	claims      remoteca.Claims
	fingerprint string
	serial      uint64
	permitPTY   bool
	source      string

	mu       sync.Mutex
	deadline time.Time
	timer    *time.Timer
	reason   string // the first close reason; non-empty once closing
	sessions map[*session]struct{}
	wg       sync.WaitGroup
}

func newConn(parent context.Context, c Config, sc *ssh.ServerConn, id *identity, source string) *conn {
	ctx, cancel := context.WithCancel(parent)
	return &conn{cfg: c, sc: sc, parent: parent, ctx: ctx, cancel: cancel, claims: id.claims, fingerprint: id.fingerprint, serial: id.serial, permitPTY: id.permitPTY, source: source, deadline: id.validBefore, sessions: map[*session]struct{}{}}
}

// serve registers the connection, arms its deadline, answers global requests
// with a refusal (no tcpip-forward, no keepalive extension) and opens
// channels until the connection ends. It returns after every session ended.
func (x *conn) serve(chans <-chan ssh.NewChannel, reqs <-chan *ssh.Request) {
	x.cfg.Registry.add(x)
	defer x.cfg.Registry.remove(x)
	x.mu.Lock()
	x.timer = time.AfterFunc(x.deadline.Sub(x.cfg.now()), x.expire)
	x.mu.Unlock()
	stop := context.AfterFunc(x.parent, func() { x.close("closed") })
	defer stop()
	x.wg.Add(1)
	go x.guard(func() {
		defer x.wg.Done()
		ssh.DiscardRequests(reqs)
	})
	func() {
		defer func() {
			if recovered(recover()) {
				x.close("closed")
				for nc := range chans {
					_ = nc.Reject(ssh.Prohibited, "closed")
				}
			}
		}()
		for nc := range chans {
			x.open(nc)
		}
	}()
	x.close("closed")
	x.wg.Wait()
}

// guard runs f and turns a panic into closing the connection.
func (x *conn) guard(f func()) {
	defer func() {
		if recovered(recover()) {
			x.close("closed")
		}
	}()
	f()
}

// open accepts only a session channel, and only while the lease still admits
// the certificate's claims and the bench has a free session slot.
func (x *conn) open(nc ssh.NewChannel) {
	if nc.ChannelType() != "session" {
		_ = nc.Reject(ssh.Prohibited, "only session channels")
		return
	}
	if _, err := x.cfg.admit(x.claims); err != nil {
		_ = nc.Reject(ssh.Prohibited, "lease not held")
		return
	}
	if !x.cfg.Registry.reserveSession(x.claims.BenchID) {
		_ = nc.Reject(ssh.ResourceShortage, "too many sessions")
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		x.cfg.Registry.releaseSession(x.claims.BenchID)
		return
	}
	s := &session{x: x, ch: ch}
	x.wg.Add(1)
	go x.guard(func() {
		defer x.wg.Done()
		defer x.cfg.Registry.releaseSession(x.claims.BenchID)
		s.serve(reqs)
	})
}

// close ends the connection once, with the first reason: its sessions are
// ended with that reason and their processes terminated.
func (x *conn) close(reason string) {
	x.mu.Lock()
	if x.reason != "" {
		x.mu.Unlock()
		return
	}
	x.reason = reason
	if x.timer != nil {
		x.timer.Stop()
	}
	sessions := make([]*session, 0, len(x.sessions))
	for s := range x.sessions {
		sessions = append(sessions, s)
	}
	x.mu.Unlock()
	// Sessions take the reason before the transport closes: closing it ends
	// their request loops, which would otherwise record "closed" first.
	for _, s := range sessions {
		s.end(reason)
	}
	_ = x.sc.Close()
	x.cancel()
}

// attach records a session that is about to start; a closing connection
// refuses it and returns its reason.
func (x *conn) attach(s *session) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.reason != "" {
		return x.reason, false
	}
	x.sessions[s] = struct{}{}
	return "", true
}

func (x *conn) detach(s *session) {
	x.mu.Lock()
	delete(x.sessions, s)
	x.mu.Unlock()
}

// expire closes the connection at its deadline, unless a renewal moved it.
func (x *conn) expire() {
	x.mu.Lock()
	if x.reason != "" {
		x.mu.Unlock()
		return
	}
	if left := x.deadline.Sub(x.cfg.now()); left > 0 {
		x.timer.Reset(left)
		x.mu.Unlock()
		return
	}
	x.mu.Unlock()
	x.close("expired")
}

func (x *conn) expiredAt(now time.Time) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	return !now.Before(x.deadline)
}

// extend moves the deadline later, never past one certificate's validity
// from now, and never on a closing connection.
func (x *conn) extend(until time.Time) {
	now := x.cfg.now()
	until = minTime(until, now.Add(remoteca.WriterValidity+remoteca.ClockSkew))
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.reason != "" || !until.After(x.deadline) {
		return
	}
	x.deadline = until
	if x.timer != nil {
		x.timer.Reset(until.Sub(now))
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
