package mirrorstage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	state, parent := filepath.Join(root, "state"), filepath.Join(root, "data", "mirrors")
	for _, p := range []string{state, parent} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	return state, parent, filepath.Join(parent, strings.Repeat("a", 64)+".git")
}

func TestCreationPinsPhysicalStageBeforeCallerEffects(t *testing.T) {
	state, parent, mirror := fixture(t)
	p, e := Create(state, parent, mirror)
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(p.State, "mirror-stages")
	var saved Intent
	raw, e := os.ReadFile(filepath.Join(dir, p.ID+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &saved); e != nil {
		t.Fatal(e)
	}
	if saved != *p || saved.Version != Version || saved.CreatedAt.IsZero() {
		t.Fatal("missing immutable reservation", saved)
	}
	var root Root
	raw, e = os.ReadFile(filepath.Join(dir, p.ID+"-root.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &root); e != nil {
		t.Fatal(e)
	}
	actual, e := identity(p.Stage)
	if e != nil {
		t.Fatal(e)
	}
	if root.ID != p.ID || root.Identity != actual {
		t.Fatal("stage not pinned before return")
	}
	entries, e := os.ReadDir(p.Stage)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
	for _, name := range []string{p.ID + ".json", p.ID + "-root.json"} {
		info, e := os.Stat(filepath.Join(dir, name))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("journal not private", info, e)
		}
	}
	if _, e = os.Lstat(mirror); !os.IsNotExist(e) {
		t.Fatal("reserved staging published a mirror", e)
	}
}

func TestUnsafeNamesAndSymlinkParentsRefuseBeforeCloneEffects(t *testing.T) {
	for _, kind := range []string{"foreign-target", "nonhashed", "linked-parent", "linked-state", "public-journal"} {
		t.Run(kind, func(t *testing.T) {
			state, parent, mirror := fixture(t)
			switch kind {
			case "foreign-target":
				mirror = filepath.Join(filepath.Dir(parent), filepath.Base(mirror))
			case "nonhashed":
				mirror = filepath.Join(parent, "other.git")
			case "linked-parent":
				if e := os.Rename(parent, parent+"-saved"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(parent+"-saved", parent); e != nil {
					t.Fatal(e)
				}
			case "linked-state":
				if e := os.Rename(state, state+"-saved"); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(state+"-saved", state); e != nil {
					t.Fatal(e)
				}
			case "public-journal":
				path := filepath.Join(state, "mirror-stages")
				if e := os.Mkdir(path, 0755); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := Create(state, parent, mirror); e == nil {
				t.Fatal("unsafe path accepted")
			}
			entries, e := os.ReadDir(parent)
			if e != nil {
				t.Fatal(e)
			}
			if len(entries) != 0 {
				t.Fatal("unsafe reservation created data", entries)
			}
		})
	}
}

func TestJournalAppendDoesNotReplaceExistingAuthority(t *testing.T) {
	dir := t.TempDir()
	if e := save(dir, "authority.json", map[string]string{"owner": "first"}); e != nil {
		t.Fatal(e)
	}
	if e := save(dir, "authority.json", map[string]string{"owner": "second"}); e == nil {
		t.Fatal("replaced authority")
	}
	raw, e := os.ReadFile(filepath.Join(dir, "authority.json"))
	if e != nil || string(raw) != "{\"owner\":\"first\"}" {
		t.Fatal(string(raw), e)
	}
}
