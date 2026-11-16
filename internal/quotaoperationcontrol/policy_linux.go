//go:build linux

package quotaoperationcontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

type policyNode struct {
	path string
	file *os.File
	info os.FileInfo
}
type policyPin struct {
	nodes []policyNode
	raw   []byte
}

// Only this fixed installed path can produce production authority. Every
// ancestor remains open, and all policy decoding/rechecking uses the same FD.
func installedPolicy() (*policyPin, hoststorage.Policy, error) {
	var zero hoststorage.Policy
	pin := &policyPin{}
	for _, path := range []string{"/", "/etc", "/etc/superbench", PolicyPath} {
		var f *os.File
		var err error
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK | syscall.O_CLOEXEC
		if path != PolicyPath {
			flags |= syscall.O_DIRECTORY
		}
		if len(pin.nodes) == 0 {
			f, err = os.OpenFile(path, flags, 0)
		} else {
			parent := pin.nodes[len(pin.nodes)-1].file
			name := "etc"
			if path == "/etc/superbench" {
				name = "superbench"
			}
			if path == PolicyPath {
				name = "storage.json"
			}
			var fd int
			fd, err = syscall.Openat(int(parent.Fd()), name, flags, 0)
			if err == nil {
				f = os.NewFile(uintptr(fd), path)
			}
		}
		if err != nil {
			pin.close()
			return nil, zero, err
		}
		info, e := f.Stat()
		if e != nil || !validPolicyNode(info, path == PolicyPath) {
			f.Close()
			pin.close()
			return nil, zero, ErrPolicy
		}
		pin.nodes = append(pin.nodes, policyNode{path, f, info})
	}
	raw, err := readPolicyFD(pin.nodes[len(pin.nodes)-1].file)
	if err != nil {
		pin.close()
		return nil, zero, err
	}
	pin.raw = raw
	var p hoststorage.Policy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&p); err == nil {
		err = p.Validate()
	}
	if err != nil || p.OperationJournal != "indexed-v2" {
		pin.close()
		if err == nil {
			err = ErrPolicy
		}
		return nil, zero, err
	}
	if err = pin.check(context.Background()); err != nil {
		pin.close()
		return nil, zero, err
	}
	runtime.SetFinalizer(pin, (*policyPin).close)
	return pin, p, nil
}

func validPolicyNode(info os.FileInfo, leaf bool) bool {
	if info == nil {
		return false
	}
	if leaf {
		return policyFile(info)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.IsDir() && st.Uid == 0 && info.Mode().Perm()&0022 == 0
}
func samePolicyNode(a, b os.FileInfo, leaf bool) bool {
	if a == nil || b == nil || !os.SameFile(a, b) || a.Mode() != b.Mode() {
		return false
	}
	x, xok := a.Sys().(*syscall.Stat_t)
	y, yok := b.Sys().(*syscall.Stat_t)
	if !xok || !yok || x.Uid != y.Uid || x.Gid != y.Gid || x.Nlink != y.Nlink {
		return false
	}
	return !leaf || x.Size == y.Size && x.Mtim == y.Mtim && x.Ctim == y.Ctim
}
func readPolicyFD(f *os.File) ([]byte, error) {
	raw, err := io.ReadAll(io.NewSectionReader(f, 0, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, ErrPolicy
	}
	return raw, nil
}
func (p *policyPin) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || len(p.nodes) != 4 || os.Getuid() != 0 || os.Geteuid() != 0 {
		return ErrPolicy
	}
	for n, node := range p.nodes {
		leaf := n == len(p.nodes)-1
		actual, err := node.file.Stat()
		named, e := os.Lstat(node.path)
		if err != nil || e != nil || !validPolicyNode(actual, leaf) || !validPolicyNode(named, leaf) || !samePolicyNode(node.info, actual, leaf) || !samePolicyNode(node.info, named, leaf) {
			return ErrPolicy
		}
	}
	raw, err := readPolicyFD(p.nodes[len(p.nodes)-1].file)
	if err != nil || !bytes.Equal(raw, p.raw) {
		return ErrPolicy
	}
	// Repeat named identity checks after the descriptor read. A replaced ancestor
	// cannot redirect decoding, even if its replacement carries identical bytes.
	for n, node := range p.nodes {
		named, e := os.Lstat(node.path)
		if e != nil || !samePolicyNode(node.info, named, n == len(p.nodes)-1) {
			return ErrPolicy
		}
	}
	return ctx.Err()
}
func (p *policyPin) close() {
	if p != nil {
		for _, n := range p.nodes {
			n.file.Close()
		}
		p.nodes = nil
	}
}
