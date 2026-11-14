package service

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type uidKey struct{}
type peerConn struct {
	net.Conn
	uid  uint32
	slot chan struct{}
	once sync.Once
}

func (c *peerConn) Close() error { e := c.Conn.Close(); c.once.Do(func() { <-c.slot }); return e }

type peerListener struct {
	net.Listener
	slots chan struct{}
}

func (l peerListener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		select {
		case l.slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		release := func() { <-l.slots }
		unix, ok := c.(*net.UnixConn)
		if !ok {
			c.Close()
			release()
			return nil, errors.New("Unix peer connection required")
		}
		raw, e := unix.SyscallConn()
		if e != nil {
			c.Close()
			release()
			return nil, e
		}
		var uid uint32
		var peerErr error
		if e = raw.Control(func(fd uintptr) { uid, peerErr = peerUID(int(fd)) }); e != nil {
			c.Close()
			release()
			return nil, e
		}
		if peerErr != nil {
			c.Close()
			release()
			return nil, peerErr
		}
		return &peerConn{Conn: c, uid: uid, slot: l.slots}, nil
	}
}
func connContext(ctx context.Context, c net.Conn) context.Context {
	p, ok := c.(*peerConn)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, uidKey{}, p.uid)
}
func listen(path, state string) (net.Listener, *os.File, error) {
	if !filepath.IsAbs(path) || !filepath.IsAbs(state) {
		return nil, nil, errors.New("absolute socket/state paths required")
	}
	if e := os.MkdirAll(state, 0700); e != nil {
		return nil, nil, e
	}
	st, e := os.Lstat(state)
	if e != nil {
		return nil, nil, e
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, nil, errors.New("service state must be private and owned by the daemon user")
	}
	lock, e := os.OpenFile(filepath.Join(state, "benchd.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, nil, e
	}
	fail := func(e error) (net.Listener, *os.File, error) { lock.Close(); return nil, nil, e }
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fail(errors.New("another benchd instance holds this state"))
	}
	parent := filepath.Dir(path)
	if e = os.MkdirAll(parent, 0750); e != nil {
		return fail(e)
	}
	st, e = os.Lstat(parent)
	if e != nil {
		return fail(e)
	}
	if !st.IsDir() || st.Mode().Perm()&0022 != 0 {
		return fail(errors.New("socket parent must not be group/world writable"))
	}
	if st, e = os.Lstat(path); e == nil {
		if st.Mode()&os.ModeSocket == 0 || st.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			return fail(errors.New("refusing to replace a non-socket or another owner's socket"))
		}
		if e = os.Remove(path); e != nil {
			return fail(e)
		}
	} else if !os.IsNotExist(e) {
		return fail(e)
	}
	base, e := net.Listen("unix", path)
	if e != nil {
		return fail(e)
	}
	if e = os.Chmod(path, 0660); e != nil {
		base.Close()
		return fail(e)
	}
	return peerListener{Listener: base, slots: make(chan struct{}, 64)}, lock, nil
}
