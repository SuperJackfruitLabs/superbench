package statepath

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbsoluteSeparatePathsAndNonexistentTails(t *testing.T) {
	root := t.TempDir()
	state, data := filepath.Join(root, "state"), filepath.Join(root, "data")
	for _, tc := range []struct {
		name, state, data string
		want              string
	}{
		{"siblings", state, data, ""},
		{"future-siblings", filepath.Join(state, "future", "tail"), filepath.Join(data, "future", "tail"), ""},
		{"prefix-is-not-nesting", state, state + "ful", ""},
		{"relative-state", "state", data, "must be absolute"},
		{"relative-data", state, "data", "must be absolute"},
		{"empty-state", "", data, "must be absolute"},
		{"identical-future", state, state, "non-nested"},
		{"data-under-state", state, filepath.Join(state, "data"), "non-nested"},
		{"state-under-data", filepath.Join(data, "state"), data, "non-nested"},
		{"nonclean-alias", state + "/../data", data, "non-nested"},
		{"filesystem-root", string(filepath.Separator), data, "non-nested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.state, tc.data)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("Validate(%q, %q) = %v, want %q", tc.state, tc.data, err, tc.want)
			}
		})
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("validation materialized future paths", entries, err)
	}
}

func TestExistingSymlinkAncestorsCannotHideOverlap(t *testing.T) {
	root := t.TempDir()
	actual, alias := filepath.Join(root, "actual"), filepath.Join(root, "alias")
	if err := os.Mkdir(actual, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, state, data string
		overlap           bool
	}{
		{"same-existing-directory", actual, alias, true},
		{"same-future-tail", filepath.Join(actual, "future", "state"), filepath.Join(alias, "future", "state"), true},
		{"future-child-hidden-by-alias", actual, filepath.Join(alias, "future", "data"), true},
		{"reverse-future-child", filepath.Join(alias, "future", "state"), actual, true},
		{"different-future-siblings", filepath.Join(actual, "state"), filepath.Join(alias, "data"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.state, tc.data)
			if (err != nil) != tc.overlap {
				t.Fatalf("got %v, want overlap %v", err, tc.overlap)
			}
			if err != nil && !strings.Contains(err.Error(), "non-nested") {
				t.Fatal("unexpected error", err)
			}
		})
	}
	if entries, err := os.ReadDir(actual); err != nil || len(entries) != 0 {
		t.Fatal("validation wrote into target", entries, err)
	}
}

func TestFilesystemResolutionErrorsArePreserved(t *testing.T) {
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	_, expected := filepath.EvalSymlinks(loop)
	if expected == nil {
		t.Fatal("fixture did not produce a filesystem resolution error")
	}
	for _, pair := range [][2]string{{loop, filepath.Join(root, "data")}, {filepath.Join(root, "state"), loop}} {
		err := Validate(pair[0], pair[1])
		if err == nil || err.Error() != expected.Error() {
			t.Fatal("filesystem resolution error was hidden", err)
		}
	}
}
