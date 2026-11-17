//go:build linux

package freshrecovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These run unprivileged: the owner is the test user, not root.
func planDir(t *testing.T) (string, uint32) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "payload-cleanups")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir, uint32(os.Getuid())
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s holds %q: %v", path, got, err)
	}
}

// A plan is replaceable until clean-bootstrap retains KEY.json; after that no
// new plan can be published, and the old plan bytes stay as they were.
func TestPlansAreReplaceableOnlyUntilAnApprovalIsRetained(t *testing.T) {
	dir, owner := planDir(t)
	if err := replacePlan(dir, "card", []byte("first"), owner, 64); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dir, "card.plan.json"), "first")
	if err := replacePlan(dir, "card", []byte("second"), owner, 64); err != nil {
		t.Fatal("plan not replaceable before retention", err)
	}
	assertFile(t, filepath.Join(dir, "card.plan.json"), "second")
	if err := os.WriteFile(filepath.Join(dir, "card.json"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replacePlan(dir, "card", []byte("third"), owner, 64); !errors.Is(err, ErrRecovery) {
		t.Fatal("plan replaced after its approval was retained", err)
	}
	assertFile(t, filepath.Join(dir, "card.plan.json"), "second")
	if err := replacePlan(dir, "other", []byte(strings.Repeat("x", 65)), owner, 64); !errors.Is(err, ErrRecovery) {
		t.Fatal("oversize plan published", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary plan files left behind: %v %v", entries, err)
	}
}

// After the leaf is gone, a retry is ours only with the retained approval of
// the same SHA-256; a plan alone, or other bytes, are refused.
func TestARetryIsAcceptedOnlyWithTheMatchingRetainedApproval(t *testing.T) {
	dir, owner := planDir(t)
	if err := replacePlan(dir, "card", []byte("reviewed"), owner, 64); err != nil {
		t.Fatal(err)
	}
	sha := digest([]byte("reviewed"))
	raw, retained, err := reviewed(dir, "card", sha, owner, 64)
	if err != nil || retained || string(raw) != "reviewed" {
		t.Fatalf("published plan not found before retention: %q %v %v", raw, retained, err)
	}
	if _, _, err = reviewed(dir, "card", digest([]byte("other")), owner, 64); !errors.Is(err, ErrRecovery) {
		t.Fatal("plan accepted under another SHA-256", err)
	}
	if err = os.WriteFile(filepath.Join(dir, "card.json"), []byte("reviewed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "card.plan.json")); err != nil {
		t.Fatal(err)
	}
	raw, retained, err = reviewed(dir, "card", sha, owner, 64)
	if err != nil || !retained || string(raw) != "reviewed" {
		t.Fatalf("retained approval not found for the retry: %q %v %v", raw, retained, err)
	}
	if err = os.WriteFile(filepath.Join(dir, "card.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = replacePlan(dir, "card", []byte("reviewed"), owner, 64); !errors.Is(err, ErrRecovery) {
		t.Fatal("plan published beside a retained approval", err)
	}
	if _, _, err = reviewed(dir, "card", sha, owner, 64); !errors.Is(err, ErrRecovery) {
		t.Fatal("a retained approval with other bytes yielded to the SHA", err)
	}
	if _, _, err = reviewed(dir, "missing", sha, owner, 64); !errors.Is(err, ErrRecovery) {
		t.Fatal("a retry without any approval accepted", err)
	}
}

func TestRetainedFilesNeedPrivateSingleLinkCustody(t *testing.T) {
	dir, owner := planDir(t)
	path := filepath.Join(dir, "card.json")
	for name, setup := range map[string]func() error{
		"group readable": func() error { return os.WriteFile(path, []byte("x"), 0640) },
		"hard link": func() error {
			if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
				return err
			}
			return os.Link(path, filepath.Join(dir, "alias"))
		},
		"symlink":   func() error { return os.Symlink(filepath.Join(dir, "target"), path) },
		"directory": func() error { return os.Mkdir(path, 0700) },
		"oversize":  func() error { return os.WriteFile(path, []byte(strings.Repeat("x", 65)), 0600) },
	} {
		os.RemoveAll(path)
		os.Remove(filepath.Join(dir, "alias"))
		if err := setup(); err != nil {
			t.Fatal(err)
		}
		if _, err := readRetained(dir, "card.json", owner, 64); err == nil {
			t.Errorf("%s retained file accepted", name)
		}
	}
	os.RemoveAll(path)
	os.Remove(filepath.Join(dir, "alias"))
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRetained(dir, "card.json", owner+1, 64); !errors.Is(err, ErrRecovery) {
		t.Error("a file of another owner accepted", err)
	}
	if raw, err := readRetained(dir, "card.json", owner, 64); err != nil || string(raw) != "x" {
		t.Error("private retained file refused", err)
	}
}
