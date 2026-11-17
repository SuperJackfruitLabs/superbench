//go:build linux

package registeredinspection

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Nodes holds every observed directory until the complete vector has
// been rechecked. No member is selected by an executable request or fake scope.
type Nodes struct {
	files   []*os.File
	initial []os.FileInfo
	root    *os.File
}

func (b *Nodes) Close() {
	for _, f := range b.files {
		_ = f.Close()
	}
	b.files = nil
	b.initial = nil
	b.root = nil
}

func (b *Nodes) retain(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return errors.Join(ErrBoundary, err)
	}
	b.files = append(b.files, f)
	b.initial = append(b.initial, info)
	return nil
}

func InspectNodes(p Policy, l Layout) (_ *Nodes, err error) {
	if p.Validate() != nil || l.Validate(p) != nil || l.Scalar {
		return nil, ErrBoundary
	}
	b := &Nodes{}
	defer func() {
		if err != nil {
			b.Close()
		}
	}()
	// Pin every physical ancestor as well as the leaf. Moving a retained leaf
	// beneath a replacement parent must not preserve the observation's identity.
	ancestry := func(path string) (*os.File, error) {
		prefix := "/"
		var current *os.File
		for _, part := range append([]string{""}, strings.Split(strings.TrimPrefix(path, "/"), "/")...) {
			prefix = filepath.Join(prefix, part)
			next, e := OpenPhysicalDirectory(prefix, p.DaemonUID)
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
	root, err := PrivateDirectory(parent, l.Key, p, l.Root)
	if err != nil {
		return nil, err
	}
	if err = b.retain(root); err != nil {
		return nil, err
	}
	b.root = root
	open := func(relative string, node Node) error {
		current := root
		for _, part := range strings.Split(relative, "/") {
			next, e := PrivateDirectory(current, part, p, Node{Device: l.Root.Device})
			if e != nil {
				return e
			}
			if e = b.retain(next); e != nil {
				return e
			}
			current = next
		}
		if !MatchesNode(current, node) {
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
func (b *Nodes) Check(p Policy, l Layout) error {
	current, err := InspectNodes(p, l)
	if err != nil {
		return err
	}
	defer current.Close()
	if len(current.files) != len(b.files) {
		return ErrBoundary
	}
	for i, f := range b.files {
		a, e := f.Stat()
		c, ce := current.files[i].Stat()
		if e != nil || ce != nil || !os.SameFile(a, c) || !os.SameFile(b.initial[i], a) || a.Mode().IsRegular() && (!SamePolicyFile(b.initial[i], a) || !SamePolicyFile(a, c)) {
			return errors.Join(ErrBoundary, e, ce)
		}
	}
	return nil
}

// Unregistered reports whether the retained aggregate root holds any entry
// outside the registered layout. It reads names only and stops at the first.
func (b *Nodes) Unregistered(l Layout) (bool, error) {
	if b == nil || b.root == nil {
		return false, ErrBoundary
	}
	fd, err := syscall.Openat(int(b.root.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false, errors.Join(ErrBoundary, err)
	}
	dir := os.NewFile(uintptr(fd), "registered aggregate root")
	defer dir.Close()
	known := map[string]bool{".superbench-git": true, ".superbench-runtime": true}
	for _, m := range l.Members {
		known[m.Name] = true
	}
	for {
		names, err := dir.Readdirnames(256)
		for _, name := range names {
			if !known[name] {
				return true, nil
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, errors.Join(ErrBoundary, err)
		}
	}
}
