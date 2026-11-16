//go:build linux

package payloadbootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

func publicationFixture(t *testing.T) (payloadexec.Policy, *os.File) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	p := payloadexec.Policy{StateRoot: root, DaemonUID: uint32(os.Geteuid()), DaemonGID: uint32(os.Getegid())}
	f, err := publicationStateRoot(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return p, f
}

func TestPublicationDirectoryUsesHeldParentAndRefusesReplacements(t *testing.T) {
	p, state := publicationFixture(t)
	dir, err := publicationDirectory(state, p)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err = checkPublicationDirectory(state, dir, p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.StateRoot, "payload-layouts")
	parked := filepath.Join(p.StateRoot, "retained-layouts")
	if err = os.Rename(path, parked); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(p.StateRoot, "unrelated")
	if err = os.Mkdir(victim, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if _, err = publicationDirectory(state, p); !errors.Is(err, syscall.ENOTDIR) && !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("symlink registration directory accepted: %v", err)
	}
	if err = checkPublicationDirectory(state, dir, p); err == nil {
		t.Fatal("replaced directory accepted")
	}
	after, err := os.Stat(victim)
	if err != nil || !sameMetadata(before, after) {
		t.Fatalf("publication touched symlink target: %v", err)
	}
	// Effects through the retained descriptor stay in the original directory.
	fd, err := syscall.Openat(int(dir.Fd()), "held-only", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		t.Fatal(err)
	}
	syscall.Close(fd)
	if _, err = os.Stat(filepath.Join(parked, "held-only")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(victim, "held-only")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descriptor followed replacement: %v", err)
	}
}

func TestPublicationRefusesUnsafeExistingDirectoryWithoutRepair(t *testing.T) {
	for _, kind := range []string{"mode", "owner", "state-symlink", "ancestor-symlink"} {
		t.Run(kind, func(t *testing.T) {
			p, state := publicationFixture(t)
			path := filepath.Join(p.StateRoot, "payload-layouts")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "mode":
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			case "owner":
				p.DaemonUID++
			case "state-symlink", "ancestor-symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(p.StateRoot, alias); err != nil {
					t.Fatal(err)
				}
				p.StateRoot = alias
				if kind == "ancestor-symlink" {
					p.StateRoot = filepath.Join(alias, "payload-layouts")
				}
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "mode" || kind == "owner" {
				if f, err := publicationDirectory(state, p); !errors.Is(err, ErrBootstrap) || f != nil {
					if f != nil {
						f.Close()
					}
					t.Fatalf("existing directory repaired: %v", err)
				}
			} else if f, err := publicationStateRoot(p); err == nil || f != nil {
				if f != nil {
					f.Close()
				}
				t.Fatalf("symlink ancestry accepted: %v", err)
			}
			after, err := os.Stat(path)
			if err != nil || !sameMetadata(before, after) {
				t.Fatalf("refusal changed existing directory: %v", err)
			}
		})
	}
}

func TestPublicationRefusesReplacedStateRootWithoutTouchingEitherTree(t *testing.T) {
	p, state := publicationFixture(t)
	dir, err := publicationDirectory(state, p)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err = checkPublicationDirectory(state, dir, p); err != nil {
		t.Fatalf("original publication custody refused: %v", err)
	}
	retained := p.StateRoot + "-retained"
	if err = os.Rename(p.StateRoot, retained); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(retained) })
	if err = os.Mkdir(p.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(p.StateRoot, "payload-layouts"), 0700); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		filepath.Join(retained, "payload-layouts", "sentinel"):    "original retained",
		filepath.Join(p.StateRoot, "payload-layouts", "sentinel"): "replacement retained",
	}
	before := make(map[string]os.FileInfo)
	for path, text := range paths {
		if err = os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		before[path], err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = checkPublicationDirectory(state, dir, p); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("replacement state root accepted: %v", err)
	}
	for path, text := range paths {
		raw, e := os.ReadFile(path)
		after, statErr := os.Stat(path)
		if e != nil || statErr != nil || string(raw) != text || !sameMetadata(before[path], after) {
			t.Fatalf("publication observer changed retained tree: %s: %q %v %v", path, raw, e, statErr)
		}
	}
}
