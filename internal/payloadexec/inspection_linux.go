//go:build linux

package payloadexec

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

// inspectedNodes holds every observed directory until the complete vector has
// been rechecked. No member is selected by an executable request or fake scope.
type inspectedNodes struct {
	files   []*os.File
	initial []os.FileInfo
}

func (b *inspectedNodes) close() {
	for _, f := range b.files {
		_ = f.Close()
	}
	b.files = nil
	b.initial = nil
}

func (b *inspectedNodes) retain(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return errors.Join(ErrBoundary, err)
	}
	b.files = append(b.files, f)
	b.initial = append(b.initial, info)
	return nil
}

func inspectNodes(p Policy, l Layout) (_ *inspectedNodes, err error) {
	if p.Validate() != nil || l.Validate(p) != nil || l.Scalar {
		return nil, ErrBoundary
	}
	b := &inspectedNodes{}
	defer func() {
		if err != nil {
			b.close()
		}
	}()
	// Pin every physical ancestor as well as the leaf. Moving a retained leaf
	// beneath a replacement parent must not preserve the observation's identity.
	ancestry := func(path string) (*os.File, error) {
		prefix := "/"
		var current *os.File
		for _, part := range append([]string{""}, strings.Split(strings.TrimPrefix(path, "/"), "/")...) {
			prefix = filepath.Join(prefix, part)
			next, e := openPhysicalDirectory(prefix, p.DaemonUID)
			if e != nil {
				return nil, errors.Join(ErrBoundary, e)
			}
			if e = b.retain(next); e != nil {
				return nil, e
			}
			current = next
		}
		return current, nil
	}
	parent, err := ancestry(filepath.Join(p.DataRoot, "benches"))
	if err != nil {
		return nil, err
	}
	registrationParent, err := ancestry(filepath.Join(p.StateRoot, "payload-layouts"))
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(registrationParent.Fd()), l.Key+".json", syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrBoundary, err)
	}
	registration := os.NewFile(uintptr(fd), "inspection registration")
	info, err := registration.Stat()
	if err != nil {
		_ = registration.Close()
		return nil, errors.Join(ErrBoundary, err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != p.DaemonUID || st.Nlink != 1 || info.Size() > MaxLayout {
		_ = registration.Close()
		return nil, ErrBoundary
	}
	if err = b.retain(registration); err != nil {
		return nil, err
	}
	root, err := privateDirectory(parent, l.Key, p, l.Root)
	if err != nil {
		return nil, err
	}
	if err = b.retain(root); err != nil {
		return nil, err
	}
	open := func(relative string, node Node) error {
		current := root
		for _, part := range strings.Split(relative, "/") {
			next, e := privateDirectory(current, part, p, Node{Device: l.Root.Device})
			if e != nil {
				return e
			}
			if e = b.retain(next); e != nil {
				return e
			}
			current = next
		}
		if !matchesNode(current, node) {
			return ErrBoundary
		}
		return nil
	}
	for _, v := range []struct {
		path string
		node Node
	}{{".superbench-runtime/.root/home", l.RootHome}, {".superbench-runtime/.root/tmp", l.RootTemp}} {
		if err = open(v.path, v.node); err != nil {
			return nil, err
		}
	}
	for _, m := range l.Members {
		for _, v := range []struct {
			path string
			node Node
		}{{m.Name, m.Checkout}, {".superbench-git/" + m.Name, m.Admin}, {".superbench-runtime/" + m.Name + "/home", m.Home}, {".superbench-runtime/" + m.Name + "/tmp", m.Temp}} {
			if err = open(v.path, v.node); err != nil {
				return nil, err
			}
		}
	}
	return b, nil
}
func (b *inspectedNodes) check(p Policy, l Layout) error {
	current, err := inspectNodes(p, l)
	if err != nil {
		return err
	}
	defer current.close()
	if len(current.files) != len(b.files) {
		return ErrBoundary
	}
	for i, f := range b.files {
		a, e := f.Stat()
		c, ce := current.files[i].Stat()
		if e != nil || ce != nil || !os.SameFile(a, c) || !os.SameFile(b.initial[i], a) || a.Mode().IsRegular() && (!samePolicyFile(b.initial[i], a) || !samePolicyFile(a, c)) {
			return errors.Join(ErrBoundary, e, ce)
		}
	}
	return nil
}

func inspectInstalled(binding helperBinding, r InspectionRequest) error {
	if r.Validate() != nil {
		return ErrRequest
	}
	p := binding.policy
	l, err := loadRegistration(p, r.Key, r.LayoutID, r.PolicyDigest)
	if err != nil {
		return err
	}
	nodes, err := inspectNodes(p, l)
	if err != nil {
		return err
	}
	defer nodes.close()
	raw, err := l.Bytes(p)
	if err != nil {
		return err
	}
	current, err := loadRegistration(p, r.Key, r.LayoutID, r.PolicyDigest)
	if err != nil || !reflect.DeepEqual(current, l) {
		return errors.Join(ErrBoundary, err)
	}
	if err = nodes.check(p, l); err != nil {
		return err
	}
	if err = binding.recheck(); err != nil {
		return err
	}
	if err = verifyStdio(p); err != nil {
		return err
	}
	nodes.close()
	return writeInspection(os.Stdout, raw, frameTimeout)
}

// The result is exactly one bounded length-prefixed canonical Layout. A reader
// that disappears or does not drain cannot keep this privileged process alive.
func writeInspection(output *os.File, raw []byte, timeout time.Duration) (err error) {
	if len(raw) == 0 || len(raw) > MaxLayout || timeout <= 0 || timeout > frameTimeout {
		return ErrFrame
	}
	fd := output.Fd()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	if errno != 0 {
		return errors.Join(ErrFrame, errno)
	}
	if _, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags|syscall.O_NONBLOCK); errno != 0 {
		return errors.Join(ErrFrame, errno)
	}
	defer func() {
		_, _, e := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_SETFL, flags)
		if e != 0 {
			err = errors.Join(ErrFrame, err, e)
		}
	}()
	duplicate, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_DUPFD_CLOEXEC, 3)
	if errno != 0 {
		return errors.Join(ErrFrame, errno)
	}
	writer := os.NewFile(duplicate, "inspection result")
	defer writer.Close()
	if err = writer.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return errors.Join(ErrFrame, err)
	}
	framed := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(framed, uint32(len(raw)))
	copy(framed[4:], raw)
	n, err := writer.Write(framed)
	if err != nil || n != len(framed) {
		return errors.Join(ErrFrame, err, io.ErrShortWrite)
	}
	return nil
}
