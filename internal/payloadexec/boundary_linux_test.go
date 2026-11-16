//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCompleteDaemonOwnedLayoutCannotBeAdoptedAsPayloadLayout(t *testing.T) {
	p, l := layoutFixture(t, false)
	p.DaemonUID, p.DaemonGID = uint32(os.Getuid()), uint32(os.Getgid())
	p.PayloadUID, p.PayloadGID = p.DaemonUID+1, p.DaemonGID+1
	base := t.TempDir()
	p.DataRoot, p.StateRoot = filepath.Join(base, "data"), filepath.Join(base, "state")
	root := filepath.Join(p.DataRoot, "benches", l.Key)
	paths := []string{root, filepath.Join(root, "api"), filepath.Join(root, ".superbench-git/api"), filepath.Join(root, ".superbench-runtime/api/home"), filepath.Join(root, ".superbench-runtime/api/tmp"), filepath.Join(root, ".superbench-runtime/.root/home"), filepath.Join(root, ".superbench-runtime/.root/tmp")}
	var nodes []Node
	for _, path := range paths {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		nodes = append(nodes, Node{Device: uint64(st.Dev), Inode: st.Ino})
	}
	l.Root, l.RootHome, l.RootTemp = nodes[0], nodes[5], nodes[6]
	l.Members[0].Checkout, l.Members[0].Admin, l.Members[0].Home, l.Members[0].Temp = nodes[1], nodes[2], nodes[3], nodes[4]
	var err error
	l.PolicyDigest, err = policyDigest(p)
	if err != nil || l.Validate(p) != nil {
		t.Fatalf("fixture must otherwise be complete: %v", err)
	}
	sentinel := filepath.Join(root, "api", "retained-dirty-file")
	if err := os.WriteFile(sentinel, []byte("preserve existing daemon-owned checkout"), 0600); err != nil {
		t.Fatal(err)
	}
	r := launchRequest()
	r.PolicyDigest = l.PolicyDigest
	opened, err := openBoundary(p, r, l)
	if opened != nil || !errors.Is(err, ErrBoundary) {
		if opened != nil {
			opened.close()
		}
		t.Fatalf("old daemon-owned complete layout was adopted: %v", err)
	}
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		if (Node{Device: uint64(st.Dev), Inode: st.Ino}) != nodes[i] || st.Uid != p.DaemonUID || info.Mode().Perm() != 0700 {
			t.Fatalf("refusal changed existing layout at %s", path)
		}
	}
	raw, err := os.ReadFile(sentinel)
	if err != nil || string(raw) != "preserve existing daemon-owned checkout" {
		t.Fatalf("refusal changed old checkout: %q %v", raw, err)
	}
}
