package gitcontrol

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlStatusPreservesIndexWhileDetectingWorkingChanges(t *testing.T) {
	client, _ := fixture(t)
	index := filepath.Join(client, ".git", "index")
	tracked := filepath.Join(client, "tracked")
	// Make the cached stat information stale without changing tracked bytes.
	// Ordinary status refreshes this index, including its physical identity.
	stamp := time.Unix(123456789, 0)
	if err := os.Chtimes(tracked, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(index)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func() {
		t.Helper()
		after, e := os.ReadFile(index)
		if e != nil {
			t.Fatal(e)
		}
		actual, e := os.Lstat(index)
		if e != nil || !os.SameFile(identity, actual) || !bytes.Equal(before, after) {
			t.Fatal("control observation rewrote the approved index", e)
		}
	}
	for range 2 {
		out, e := run(t, client, true, "status", "--porcelain", "--untracked-files=all")
		if e != nil || len(out) != 0 {
			t.Fatal("unchanged contents reported dirty", string(out), e)
		}
		unchanged()
	}
	// Establish that this fixture really exercises Git's optional index refresh.
	if out, e := run(t, client, false, "status", "--porcelain"); e != nil || len(out) != 0 {
		t.Fatal(string(out), e)
	}
	refreshed, err := os.ReadFile(index)
	if err != nil || bytes.Equal(before, refreshed) {
		t.Fatal("ordinary status did not exercise index refresh", err)
	}
	before = refreshed
	identity, err = os.Lstat(index)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(tracked, []byte("changed tracked contents"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, client, true, "status", "--porcelain", "--untracked-files=all")
	if err != nil || !strings.Contains(string(out), "M tracked") {
		t.Fatal("read-only control status missed changed contents", string(out), err)
	}
	unchanged()
}
