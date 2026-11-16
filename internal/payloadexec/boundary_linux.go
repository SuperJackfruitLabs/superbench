//go:build linux

package payloadexec

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"os"
	"path/filepath"
	"strings"
)

type boundary struct {
	policy                       Policy
	request                      Request
	layout                       Layout
	root, cwd, home, temp, admin *os.File
	files                        []*os.File
}

func (b *boundary) close() {
	for _, file := range b.files {
		_ = file.Close()
	}
}

// This opens only already-completed private layout nodes. It cannot create,
// chmod, chown, adopt or repair an old shared worktree layout.
func openBoundary(p Policy, r Request, l Layout) (_ *boundary, err error) {
	if p.Validate() != nil || r.Validate() != nil || l.Validate(p) != nil || l.ID != r.LayoutID || l.Key != r.Key || l.PolicyDigest != r.PolicyDigest || l.Scalar && r.Root || !l.Scalar && r.Member == "" && !r.Root {
		return nil, ErrBoundary
	}
	b := &boundary{policy: p, request: r, layout: l}
	defer func() {
		if err != nil {
			b.close()
		}
	}()
	parent, err := openPhysicalDirectory(filepath.Join(p.DataRoot, "benches"), p.DaemonUID)
	if err != nil {
		return nil, errors.Join(ErrBoundary, err)
	}
	defer parent.Close()
	b.root, err = privateDirectory(parent, l.Key, p, l.Root)
	if err != nil {
		return nil, err
	}
	b.files = append(b.files, b.root)
	open := func(relative string, expected Node) (*os.File, error) {
		current := b.root
		for _, part := range strings.Split(relative, "/") {
			opened, err := privateDirectory(current, part, p, Node{Device: l.Root.Device})
			if err != nil {
				return nil, err
			}
			b.files = append(b.files, opened)
			current = opened
		}
		if !matchesNode(current, expected) {
			return nil, ErrBoundary
		}
		return current, nil
	}
	if r.Root {
		b.cwd = b.root
		b.home, err = open(".superbench-runtime/.root/home", l.RootHome)
		if err != nil {
			return nil, err
		}
		b.temp, err = open(".superbench-runtime/.root/tmp", l.RootTemp)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	var selected *LayoutMember
	for i := range l.Members {
		if l.Members[i].Name == r.Member {
			selected = &l.Members[i]
		}
	}
	if selected == nil {
		return nil, ErrBoundary
	}
	part := selected.Name
	if l.Scalar {
		part = "worktree"
	}
	b.cwd, err = open(part, selected.Checkout)
	if err != nil {
		return nil, err
	}
	b.admin, err = open(".superbench-git/"+part, selected.Admin)
	if err != nil {
		return nil, err
	}
	b.home, err = open(".superbench-runtime/"+part+"/home", selected.Home)
	if err != nil {
		return nil, err
	}
	b.temp, err = open(".superbench-runtime/"+part+"/tmp", selected.Temp)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func privateDirectory(parent *os.File, name string, p Policy, expected Node) (*os.File, error) {
	return registeredinspection.PrivateDirectory(parent, name, p, expected)
}
func matchesNode(file *os.File, expected Node) bool {
	return registeredinspection.MatchesNode(file, expected)
}

func (b *boundary) check() error {
	current, err := openBoundary(b.policy, b.request, b.layout)
	if err != nil {
		return err
	}
	defer current.close()
	for _, pair := range [][2]*os.File{{b.root, current.root}, {b.cwd, current.cwd}, {b.home, current.home}, {b.temp, current.temp}, {b.admin, current.admin}} {
		if pair[0] == nil && pair[1] == nil {
			continue
		}
		if pair[0] == nil || pair[1] == nil {
			return ErrBoundary
		}
		a, err := pair[0].Stat()
		if err != nil {
			return errors.Join(ErrBoundary, err)
		}
		c, err := pair[1].Stat()
		if err != nil || !os.SameFile(a, c) {
			return errors.Join(ErrBoundary, err)
		}
	}
	return nil
}
