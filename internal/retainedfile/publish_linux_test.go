//go:build linux

package retainedfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// These tests run unprivileged: the owner-aware seam expects the test user, and
// the path-level root custody check is replaced by a no-op. The held-directory,
// temp, link and existing-file rules are the production code. Root custody and a
// real ledger are proven by the privileged freshstoragefixture.
const limit = 64

var raw = []byte(`{"approved":"exact"}`)

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func put(dir, name string, data []byte) error {
	return publish(uint32(os.Getuid()), func(string) error { return nil }, dir, name, data, limit)
}

// entries lists the directory, so tests can see that no temp file is left.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func requireRetained(t *testing.T, path string, want []byte) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Nlink != 1 || int(st.Uid) != os.Getuid() {
		t.Fatalf("not a retained file: mode=%v nlink=%d uid=%d", info.Mode(), st.Nlink, st.Uid)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("bytes=%q err=%v", got, err)
	}
	return info
}

func TestFirstPublishAndExactRetry(t *testing.T) {
	dir := fixtureDir(t)
	path := filepath.Join(dir, "card.json")
	if err := put(dir, "card.json", raw); err != nil {
		t.Fatal(err)
	}
	before := requireRetained(t, path, raw)
	for n := 0; n < 2; n++ {
		if err := put(dir, "card.json", raw); err != nil {
			t.Fatal("exact retry refused", err)
		}
	}
	after := requireRetained(t, path, raw)
	if !os.SameFile(before, after) || before.ModTime() != after.ModTime() {
		t.Fatal("retry replaced the approval")
	}
	if got := entries(t, dir); len(got) != 1 || got[0] != "card.json" {
		t.Fatal("temp file left behind", got)
	}
}

func TestExistingWithDifferentBytesRefuses(t *testing.T) {
	dir := fixtureDir(t)
	path := filepath.Join(dir, "card.json")
	if err := put(dir, "card.json", raw); err != nil {
		t.Fatal(err)
	}
	for _, other := range [][]byte{[]byte(`{"approved":"other"}`), append(append([]byte{}, raw...), ' '), raw[:len(raw)-1]} {
		if err := put(dir, "card.json", other); !errors.Is(err, ErrRefused) {
			t.Fatalf("different approval %q accepted: %v", other, err)
		}
	}
	requireRetained(t, path, raw)
	if got := entries(t, dir); len(got) != 1 {
		t.Fatal("temp file left behind", got)
	}
}

func TestUnsafeExistingFileRefuses(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "mode", "hardlink", "partial", "oversize", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			dir := fixtureDir(t)
			good := filepath.Join(dir, "good.json")
			if err := os.WriteFile(good, raw, 0600); err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(dir, "bad.json")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(good, bad)
			case "directory":
				err = os.Mkdir(bad, 0700)
			case "mode":
				if err = os.WriteFile(bad, raw, 0600); err == nil {
					err = os.Chmod(bad, 0644)
				}
			case "hardlink":
				err = os.Link(good, bad)
			case "partial":
				err = os.WriteFile(bad, raw[:1], 0600)
			case "oversize":
				err = os.WriteFile(bad, bytes.Repeat([]byte("x"), limit+10), 0600)
			case "fifo":
				err = syscall.Mkfifo(bad, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := put(dir, "bad.json", raw); !errors.Is(err, ErrRefused) {
				t.Fatal("unsafe existing file accepted", err)
			}
			if got := entries(t, dir); len(got) != 2 {
				t.Fatal("refusal left files", got)
			}
			if kind == "hardlink" {
				if st, _ := os.Lstat(good); st.Sys().(*syscall.Stat_t).Nlink != 2 {
					t.Fatal("refusal removed a foreign link")
				}
			}
		})
	}
}

func TestStaleTempFileDoesNotAffectOutcome(t *testing.T) {
	dir := fixtureDir(t)
	stale := filepath.Join(dir, ".card.json"+tempMarker+"0123")
	if err := os.WriteFile(stale, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := put(dir, "card.json", raw); err != nil {
		t.Fatal(err)
	}
	if err := put(dir, "card.json", raw); err != nil {
		t.Fatal(err)
	}
	requireRetained(t, filepath.Join(dir, "card.json"), raw)
	if got, err := os.ReadFile(stale); err != nil || string(got) != "{" {
		t.Fatal("unrelated stale temp changed", err)
	}
	if err := put(dir, "card.json", []byte(`{"approved":"other"}`)); !errors.Is(err, ErrRefused) {
		t.Fatal("stale temp let a different approval in", err)
	}
}

// A crash after the temp write but before link leaves only a temp file.
func TestCrashBeforeLinkRecovers(t *testing.T) {
	dir := fixtureDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".card.json"+tempMarker+"crashed"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := put(dir, "card.json", raw); err != nil {
		t.Fatal(err)
	}
	requireRetained(t, filepath.Join(dir, "card.json"), raw)
}

// A crash after link but before unlink leaves the final file with two links.
// The exact retry removes this name's own temp link; a foreign link still refuses.
func TestCrashBeforeUnlinkRecovers(t *testing.T) {
	dir := fixtureDir(t)
	path := filepath.Join(dir, "card.json")
	if err := put(dir, "card.json", raw); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, ".card.json"+tempMarker+"crashed")
	if err := os.Link(path, temp); err != nil {
		t.Fatal(err)
	}
	if err := put(dir, "card.json", raw); err != nil {
		t.Fatal("crash after link wedged the retry", err)
	}
	requireRetained(t, path, raw)
	if _, err := os.Lstat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale temp link kept", err)
	}
	foreign := filepath.Join(dir, ".other.json"+tempMarker+"crashed")
	if err := os.Link(path, foreign); err != nil {
		t.Fatal(err)
	}
	if err := put(dir, "card.json", raw); !errors.Is(err, ErrRefused) {
		t.Fatal("foreign link accepted", err)
	}
	if err := put(dir, "card.json", []byte(`{"approved":"other"}`)); !errors.Is(err, ErrRefused) {
		t.Fatal("different approval accepted", err)
	}
}

func TestSizeAndNameRefuse(t *testing.T) {
	dir := fixtureDir(t)
	for _, data := range [][]byte{nil, {}, bytes.Repeat([]byte("x"), limit+1)} {
		if err := put(dir, "card.json", data); !errors.Is(err, ErrRefused) {
			t.Fatalf("size %d accepted: %v", len(data), err)
		}
	}
	if err := put(dir, "edge.json", bytes.Repeat([]byte("x"), limit)); err != nil {
		t.Fatal("size at the limit refused", err)
	}
	if err := publish(uint32(os.Getuid()), func(string) error { return nil }, dir, "zero.json", raw, 0); !errors.Is(err, ErrRefused) {
		t.Fatal("zero limit accepted", err)
	}
	for _, name := range []string{"", ".", "..", "a/b", "../card.json", "/card.json", ".hidden", "a\x00b", strings.Repeat("a", maxName+1)} {
		if err := put(dir, name, raw); !errors.Is(err, ErrRefused) {
			t.Fatalf("name %q accepted: %v", name, err)
		}
	}
	if got := entries(t, dir); len(got) != 1 || got[0] != "edge.json" {
		t.Fatal("refusals wrote files", got)
	}
}

func TestDirectoryMustBePrivateAndReal(t *testing.T) {
	dir := fixtureDir(t)
	for _, mode := range []os.FileMode{0750, 0705, 0770} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if err := put(dir, "card.json", raw); !errors.Is(err, ErrRefused) {
			t.Fatalf("directory mode %v accepted: %v", mode, err)
		}
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(fixtureDir(t), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := put(link, "card.json", raw); err == nil {
		t.Fatal("symlinked directory accepted")
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Fatal("refusals wrote files", got)
	}
	want := errors.New("custody refused")
	if err := publish(uint32(os.Getuid()), func(string) error { return want }, dir, "card.json", raw, limit); err != want {
		t.Fatal("directory custody check skipped", err)
	}
}

func TestCallerMustBeDirect(t *testing.T) {
	dir := fixtureDir(t)
	if os.Getuid() != 0 {
		if err := Publish(dir, "card.json", raw, limit); !errors.Is(err, ErrRefused) {
			t.Fatal("non-root caller accepted", err)
		}
		if err := DirectRoot(); !errors.Is(err, ErrRefused) {
			t.Fatal("non-root counted as root", err)
		}
	}
	if err := publish(uint32(os.Getuid())+1, func(string) error { return nil }, dir, "card.json", raw, limit); !errors.Is(err, ErrRefused) {
		t.Fatal("wrong uid accepted", err)
	}
	t.Setenv("SUDO_UID", "1000")
	if err := put(dir, "card.json", raw); !errors.Is(err, ErrRefused) {
		t.Fatal("sudo caller accepted", err)
	}
	if got := entries(t, dir); len(got) != 0 {
		t.Fatal("refusals wrote files", got)
	}
}
