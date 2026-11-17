//go:build linux

package registeredinspection

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection/layout"
)

func TestUnregisteredRootEntryIsReportedWithoutItsName(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".superbench-git", ".superbench-runtime", "api", "web"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	l := Layout{Members: []layout.LayoutMember{{Name: "api"}, {Name: "web"}}}
	open := func() *Nodes {
		f, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return &Nodes{root: f}
	}
	if dirty, err := open().Unregistered(l); err != nil || dirty {
		t.Fatalf("registered-only root reported dirty: %v %v", dirty, err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if dirty, err := open().Unregistered(l); err != nil || !dirty {
		t.Fatalf("unregistered root file was not reported: %v %v", dirty, err)
	}
	if _, err := (&Nodes{}).Unregistered(l); !errors.Is(err, ErrBoundary) {
		t.Fatalf("closed nodes observed a root: %v", err)
	}
}
