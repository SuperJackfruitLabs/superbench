//go:build linux

package payloadbootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

const maxNodes = 65536
const openPath = 0x200000
const atEmptyPath = 0x1000
const atSymlinkNoFollow = 0x100

type treeEntry struct {
	relative     string
	file, parent *os.File
	name         string
	before       os.FileInfo
}
type tree struct {
	entries  []treeEntry
	nodes    map[string]payloadexec.Node
	parent   *os.File
	children map[uintptr][]string
}

func (t *tree) close() {
	for _, e := range t.entries {
		_ = e.file.Close()
	}
	if t.parent != nil {
		_ = t.parent.Close()
	}
}
func sameMetadata(a, b os.FileInfo) bool {
	x, y := a.Sys().(*syscall.Stat_t), b.Sys().(*syscall.Stat_t)
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && x.Uid == y.Uid && x.Gid == y.Gid && x.Nlink == y.Nlink && x.Mtim == y.Mtim && x.Ctim == y.Ctim
}
func (t *tree) check() error {
	for _, e := range t.entries {
		now, err := e.file.Stat()
		if err != nil || !sameMetadata(e.before, now) {
			return ErrBootstrap
		}
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
		if e.before.IsDir() {
			flags |= syscall.O_DIRECTORY
		} else if e.before.Mode()&os.ModeSymlink != 0 {
			flags = openPath | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
		}
		fd, err := syscall.Openat(int(e.parent.Fd()), e.name, flags, 0)
		if err != nil {
			return err
		}
		current := os.NewFile(uintptr(fd), e.name)
		info, err := current.Stat()
		_ = current.Close()
		if err != nil || !sameMetadata(e.before, info) {
			return ErrBootstrap
		}
		if e.before.IsDir() {
			names, err := e.file.Readdirnames(-1)
			if err != nil {
				return err
			}
			if _, err = e.file.Seek(0, 0); err != nil {
				return err
			}
			expected := append([]string(nil), t.children[e.file.Fd()]...)
			sort.Strings(names)
			sort.Strings(expected)
			if strings.Join(names, "\x00") != strings.Join(expected, "\x00") {
				return ErrBootstrap
			}
		}
	}
	return nil
}
func (m *Manager) capture(i intent) (out *tree, err error) {
	if err = m.checkRoot(i, m.policy.DaemonUID); err != nil {
		return nil, err
	}
	parent, err := os.Open(filepath.Dir(m.root(i.Spec)))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	t := &tree{nodes: map[string]payloadexec.Node{}, children: map[uintptr][]string{}}
	defer func() {
		if err != nil {
			t.close()
		}
	}()
	seen := map[payloadexec.Node]bool{}
	var walk func(*os.File, string, string) error
	walk = func(parent *os.File, name, relative string) error {
		if len(t.entries) >= maxNodes || len(relative) > 4096 {
			return ErrBootstrap
		}
		fd, e := syscall.Openat(int(parent.Fd()), name, openPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(fd), name)
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		if info.IsDir() || info.Mode().IsRegular() {
			f.Close()
			flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
			if info.IsDir() {
				flags |= syscall.O_DIRECTORY
			}
			fd, e = syscall.Openat(int(parent.Fd()), name, flags, 0)
			if e != nil {
				return e
			}
			f = os.NewFile(uintptr(fd), name)
			actual, e := f.Stat()
			if e != nil || !sameMetadata(info, actual) {
				f.Close()
				return ErrBootstrap
			}
			info = actual
		}
		st := info.Sys().(*syscall.Stat_t)
		identity := node(info)
		if st.Uid != m.policy.DaemonUID || st.Gid != m.policy.DaemonGID || identity.Device != i.Root.Device || seen[identity] || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 || !info.IsDir() && st.Nlink != 1 {
			f.Close()
			return ErrBootstrap
		}
		// No external object database, linked worktree registration or replacement
		// objects can survive the independent-admin materialization boundary.
		if strings.HasPrefix(relative, ".superbench-git/") && (strings.HasSuffix(relative, "/objects/info/alternates") || strings.Contains(relative, "/worktrees") || strings.Contains(relative, "/refs/replace")) {
			f.Close()
			return ErrBootstrap
		}
		seen[identity] = true
		t.nodes[relative] = identity
		t.children[parent.Fd()] = append(t.children[parent.Fd()], name)
		t.entries = append(t.entries, treeEntry{relative, f, parent, name, info})
		if info.IsDir() {
			names, e := f.Readdirnames(-1)
			if e != nil {
				return e
			}
			if _, e = f.Seek(0, 0); e != nil {
				return e
			}
			sort.Strings(names)
			for _, child := range names {
				if child == "." || child == ".." || strings.Contains(child, "/") {
					return ErrBootstrap
				}
				next := child
				if relative != "" {
					next = relative + "/" + child
				}
				if e = walk(f, child, next); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if err = walk(parent, i.Spec.Key, ""); err != nil {
		return nil, err
	}
	// Retain the protected parent descriptor until the entire inventory closes.
	copyFD, err := syscall.Dup(int(parent.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(copyFD)
	ownedParent := os.NewFile(uintptr(copyFD), "benches")
	t.entries[0].parent = ownedParent
	t.parent = ownedParent
	return t, nil
}
func (t *tree) layout(m *Manager, s Spec) (payloadexec.Layout, error) {
	l := payloadexec.Layout{Version: payloadexec.LayoutVersion, Key: s.Key, ID: s.ID, PolicyDigest: m.digest, Root: t.nodes[""], RootHome: t.nodes[".superbench-runtime/.root/home"], RootTemp: t.nodes[".superbench-runtime/.root/tmp"]}
	for _, member := range s.Members {
		base := ".superbench-runtime/" + member.Name
		l.Members = append(l.Members, payloadexec.LayoutMember{Name: member.Name, Checkout: t.nodes[member.Name], Admin: t.nodes[".superbench-git/"+member.Name], Home: t.nodes[base+"/home"], Temp: t.nodes[base+"/tmp"]})
	}
	return l, l.Validate(m.policy)
}
func (t *tree) transfer(ctx context.Context, m *Manager) error {
	// Children first; the root stays daemon-private until every child transition
	// has finished. Operate exclusively on held descriptors, never recursive paths.
	for j := len(t.entries) - 1; j >= 0; j-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := &t.entries[j]
		now, err := e.file.Stat()
		if err != nil || !sameMetadata(e.before, now) {
			return errors.Join(ErrBootstrap, err)
		}
		if e.before.Mode()&os.ModeSymlink != 0 {
			if err := syscall.Fchownat(int(e.file.Fd()), "", int(m.policy.PayloadUID), int(m.policy.PayloadGID), atEmptyPath|atSymlinkNoFollow); err != nil {
				return err
			}
		} else {
			if err := e.file.Chown(int(m.policy.PayloadUID), int(m.policy.PayloadGID)); err != nil {
				return err
			}
			mode := e.before.Mode().Perm() & 0755
			if e.before.IsDir() {
				mode = 0700
			}
			if err := e.file.Chmod(mode); err != nil {
				return err
			}
			if err := e.file.Sync(); err != nil {
				return err
			}
		}
		after, err := e.file.Stat()
		if err != nil || !os.SameFile(e.before, after) {
			return errors.Join(ErrBootstrap, err)
		}
		e.before = after
	}
	return nil
}

func (t *tree) topology(s Spec) error {
	expected := map[string][]string{"": {".superbench-git", ".superbench-runtime"}, ".superbench-runtime": {".root"}, ".superbench-runtime/.root": {"home", "tmp"}}
	for _, member := range s.Members {
		expected[""] = append(expected[""], member.Name)
		expected[".superbench-git"] = append(expected[".superbench-git"], member.Name)
		expected[".superbench-runtime"] = append(expected[".superbench-runtime"], member.Name)
		expected[".superbench-runtime/"+member.Name] = []string{"home", "tmp"}
	}
	byPath := map[string]treeEntry{}
	for _, e := range t.entries {
		byPath[e.relative] = e
	}
	for path, want := range expected {
		e, ok := byPath[path]
		if !ok || !e.before.IsDir() {
			return ErrBootstrap
		}
		got := append([]string(nil), t.children[e.file.Fd()]...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			return ErrBootstrap
		}
	}
	for _, member := range s.Members {
		for _, path := range []string{member.Name, ".superbench-git/" + member.Name, ".superbench-runtime/" + member.Name + "/home", ".superbench-runtime/" + member.Name + "/tmp"} {
			e, ok := byPath[path]
			if !ok || !e.before.IsDir() {
				return ErrBootstrap
			}
		}
		dot, ok := byPath[member.Name+"/.git"]
		if !ok || !dot.before.Mode().IsRegular() || dot.before.Size() > 8192 {
			return ErrBootstrap
		}
	}
	return nil
}
