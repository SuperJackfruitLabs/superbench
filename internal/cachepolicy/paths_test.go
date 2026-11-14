package cachepolicy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSharedDefaultsAndPrivateSelections(t *testing.T) {
	data, checkout := filepath.Join(t.TempDir(), "data"), filepath.Join(t.TempDir(), "task")
	for _, path := range []string{data, checkout} {
		if e := os.Mkdir(path, 0700); e != nil {
			t.Fatal(e)
		}
	}
	defaults, e := Prepare(data, checkout, Settings{})
	if e != nil || defaults.Pnpm != filepath.Join(data, "store", "pnpm") || defaults.GoMod != filepath.Join(data, "store", "go-mod") {
		t.Fatal(defaults, e)
	}
	no := false
	private, e := Prepare(data, checkout, Settings{&no, &no, &no, &no, &no})
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{private.Pnpm, private.Npm, private.Bun, private.Playwright, private.GoBuild, private.GoMod} {
		rel, e := filepath.Rel(filepath.Join(checkout, ".bench", "cache"), path)
		if e != nil || filepath.Dir(rel) != "." {
			t.Fatal("private cache escaped checkout", path, e)
		}
		if info, e := os.Lstat(path); e != nil || !info.IsDir() {
			t.Fatal(path, e)
		}
	}
	if again, e := Prepare(data, checkout, Settings{&no, &no, &no, &no, &no}); e != nil || again != private {
		t.Fatal(again, e)
	}
}

func TestCachePrefixesRejectLinksAndNonDirectories(t *testing.T) {
	for _, kind := range []string{"shared-root", "shared-tool", "private-parent", "private-root", "private-tool", "file"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			data, checkout, outside := filepath.Join(root, "data"), filepath.Join(root, "task"), filepath.Join(root, "outside")
			for _, path := range []string{data, checkout, outside} {
				if e := os.Mkdir(path, 0700); e != nil {
					t.Fatal(e)
				}
			}
			settings := Settings{}
			path := filepath.Join(data, "store")
			no := false
			if kind == "shared-tool" {
				path = filepath.Join(path, "pnpm")
			}
			if kind == "private-parent" || kind == "private-root" || kind == "private-tool" || kind == "file" {
				settings.Pnpm = &no
				path = filepath.Join(checkout, ".bench")
				if kind != "private-parent" {
					path = filepath.Join(path, "cache")
				}
				if kind == "private-tool" || kind == "file" {
					path = filepath.Join(path, "pnpm")
				}
			}
			if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				t.Fatal(e)
			}
			var e error
			if kind == "file" {
				e = os.WriteFile(path, []byte("keep"), 0600)
			} else {
				e = os.Symlink(outside, path)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = Prepare(data, checkout, settings); e == nil {
				t.Fatal("unsafe cache prefix accepted")
			}
			entries, e := os.ReadDir(outside)
			if e != nil || len(entries) != 0 {
				t.Fatal("cache preparation escaped through link", entries, e)
			}
		})
	}
}
