//go:build linux

package payloadbootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

func captureFixture(t *testing.T) (*Manager, intent) {
	t.Helper()
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := payloadexec.Policy{DaemonUID: uint32(os.Geteuid()), DaemonGID: uint32(os.Getegid()), PayloadUID: uint32(os.Geteuid() + 1), PayloadGID: uint32(os.Getegid() + 1), DataRoot: data, StateRoot: data + "-state", CgroupRoot: "/sys/fs/cgroup/fixture"}
	m := &Manager{policy: p}
	s := Spec{Version: Version, Key: "card", ID: strings.Repeat("a", 32), CheckpointRepository: "api", Members: []Member{{Name: "api", Repository: "https://example.invalid/repo.git", Branch: "card/task", Source: strings.Repeat("b", 40), ObjectFormat: "sha1"}}}
	root := m.root(s)
	for _, relative := range []string{"", ".superbench-git/api", ".superbench-runtime/.root/home", ".superbench-runtime/.root/tmp", ".superbench-runtime/api/home", ".superbench-runtime/api/tmp", "api"} {
		if err = os.MkdirAll(filepath.Join(root, relative), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(root, "api/.git"), []byte("gitdir: "+filepath.Join(root, ".superbench-git/api")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	return m, intent{Spec: s, Root: node(info), Phase: "staged"}
}
func TestInventoryRefusesNewEdgesAndReplacementBeforeTransfer(t *testing.T) {
	m, i := captureFixture(t)
	tree, err := m.capture(i)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	if err = tree.topology(i.Spec); err != nil {
		t.Fatal(err)
	}
	if err = tree.check(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.root(i.Spec), "api/new-untracked")
	if err = os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = tree.check(); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("changed captured edges: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "preserve" {
		t.Fatalf("observer changed work: %q %v", raw, err)
	}
}
func TestInventoryRefusesHardlinkAndExternalGitObjects(t *testing.T) {
	for _, kind := range []string{"hardlink", "alternates", "special", "foreign-root-entry"} {
		t.Run(kind, func(t *testing.T) {
			m, i := captureFixture(t)
			root := m.root(i.Spec)
			switch kind {
			case "hardlink":
				a := filepath.Join(root, "api/file")
				if err := os.WriteFile(a, []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(a, filepath.Join(root, "api/linked")); err != nil {
					t.Fatal(err)
				}
			case "alternates":
				dir := filepath.Join(root, ".superbench-git/api/objects/info")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "alternates"), []byte("/unrelated/objects\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "special":
				if err := os.Symlink("../api", filepath.Join(root, ".superbench-runtime/api/home-link")); err != nil {
					t.Fatal(err)
				}
			case "foreign-root-entry":
				if err := os.WriteFile(filepath.Join(root, "unfamiliar"), []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			captured, err := m.capture(i)
			if captured != nil {
				defer captured.close()
			}
			if kind == "special" || kind == "foreign-root-entry" {
				if err != nil {
					t.Fatal(err)
				}
				err = captured.topology(i.Spec)
			}
			if !errors.Is(err, ErrBootstrap) {
				t.Fatalf("unsafe topology %s: %v", kind, err)
			}
		})
	}
}
func TestZeroManagerCannotBeginOrComplete(t *testing.T) {
	var m Manager
	s := validSpecForTest()
	if err := m.Begin(t.Context(), s); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("zero begin: %v", err)
	}
	if err := m.Materialize(t.Context(), s); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("zero materialize: %v", err)
	}
	var absent *Manager
	if err := absent.Begin(t.Context(), s); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("nil begin: %v", err)
	}
	if l, err := m.Complete(t.Context(), s); !errors.Is(err, ErrBootstrap) || l.ID != "" {
		t.Fatalf("zero complete disclosed layout: %#v %v", l, err)
	}
}

func TestPinnedInventoryRejectsReplacementWithoutTouchingEitherTree(t *testing.T) {
	m, i := captureFixture(t)
	original := filepath.Join(m.root(i.Spec), "api")
	if err := os.WriteFile(filepath.Join(original, "sentinel"), []byte("original retained"), 0600); err != nil {
		t.Fatal(err)
	}
	tree, err := m.capture(i)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.close()
	if err := tree.topology(i.Spec); err != nil {
		t.Fatal(err)
	}
	if err := tree.check(); err != nil {
		t.Fatalf("valid pinned inventory refused before replacement: %v", err)
	}
	parked := filepath.Join(filepath.Dir(m.root(i.Spec)), "parked-fixture-api")
	if err := os.Rename(original, parked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(original, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "sentinel"), []byte("replacement retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tree.check(); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("replacement accepted: %v", err)
	}
	for path, want := range map[string]string{parked: "original retained", original: "replacement retained"} {
		raw, err := os.ReadFile(filepath.Join(path, "sentinel"))
		if err != nil || string(raw) != want {
			t.Fatalf("observer altered %s: %q %v", path, raw, err)
		}
	}
}
