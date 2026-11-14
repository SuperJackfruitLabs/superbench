package reap

import (
	"os"
	"path/filepath"
	"testing"
)

func put(t *testing.T, path, text string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestManifestPreservesSymlinkTargetsAndPartialProgress(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	put(t, outside, "keep")
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(root, "tracked"), "recoverable")
	put(t, filepath.Join(root, "artifact"), "review me")
	plan, e := Snapshot(root, map[string]bool{"tracked": true})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(root, "tracked")); e != nil {
		t.Fatal(e)
	}
	if e = DeleteContents(root, plan); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(outside)
	if e != nil || string(data) != "keep" {
		t.Fatal("followed deletion symlink", e)
	}
}
func TestChangedIgnoredFileAndNewEntryPreventAnyDeletion(t *testing.T) {
	for _, kind := range []string{"changed", "new", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			put(t, filepath.Join(root, "first"), "keep")
			put(t, filepath.Join(root, "last"), "artifact")
			plan, e := Snapshot(root, nil)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "changed":
				put(t, filepath.Join(root, "last"), "changed")
			case "new":
				put(t, filepath.Join(root, "new"), "new")
			case "symlink":
				if e = os.Remove(filepath.Join(root, "last")); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink("first", filepath.Join(root, "last")); e != nil {
					t.Fatal(e)
				}
			}
			if e = DeleteContents(root, plan); e == nil {
				t.Fatal("changed directory deleted")
			}
			if _, e = os.Stat(filepath.Join(root, "first")); e != nil {
				t.Fatal("preflight deleted before refusal", e)
			}
		})
	}
}

func TestMountedRootIsRefused(t *testing.T) {
	// /dev is a mounted API filesystem on supported Linux/macOS hosts. Test only
	// mount admission; never walk its device nodes.
	if e := rejectMounts("/dev"); e == nil {
		t.Fatal("mounted reap root accepted")
	}
}
