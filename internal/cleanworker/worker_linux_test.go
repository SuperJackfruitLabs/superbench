//go:build linux

// The runtime otherwise keeps the cgroup cpu.max file open for GOMAXPROCS
// updates, and the worker's descriptor check would refuse it. bench-quota's
// main package carries the same directive.
//go:debug containermaxprocs=0

package cleanworker

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

// The test binary doubles as the worker so a real child runs the self-checks.
func TestMain(m *testing.M) {
	if os.Getenv("SUPERBENCH_CLEAN_WORKER_TEST_CHILD") == "1" {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// child runs the worker as the test user with a clean environment and Dir=/.
func child(t *testing.T, args []string, extra []*os.File, setsid bool, request []byte) (string, string, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := &exec.Cmd{Path: exe, Args: append([]string{"bench-quota"}, args...), Env: []string{"SUPERBENCH_CLEAN_WORKER_TEST_CHILD=1"}, Dir: "/", ExtraFiles: extra, Stdin: bytes.NewReader(request),
		SysProcAttr: &syscall.SysProcAttr{Setsid: setsid}}
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	err = cmd.Run()
	return out.String(), diagnostic.String(), err
}

func self() []string {
	return Arguments(Identity{uint32(os.Getuid()), uint32(os.Getgid()), 0})
}

// Run as the ordinary test user, the real worker refuses an extra descriptor,
// its parent's session, a uid or gid it does not hold, and, because the test user keeps its bounding
// set (and possibly supplementary groups), even its own identity. It reads no
// request and writes no reply when it refuses.
func TestTheRealWorkerRefusesBeforeReadingItsRequest(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("ordinary test requires an unprivileged user")
	}
	root := t.TempDir()
	request, err := EncodeRequest(Request{Version, Observe, root, nil})
	if err != nil {
		t.Fatal(err)
	}
	extra, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	other := Identity{uint32(os.Getuid()) + 1, uint32(os.Getgid()), 0}
	otherGroup := Identity{uint32(os.Getuid()), uint32(os.Getgid()) + 1, 0}
	for name, c := range map[string]struct {
		args   []string
		extra  []*os.File
		setsid bool
		reason string
	}{
		"extra-descriptor": {self(), []*os.File{extra}, true, "descriptors"},
		"parent-session":   {self(), nil, false, ": session"},
		"wrong-uid":        {Arguments(other), nil, true, ": Uid"},
		"wrong-gid":        {Arguments(otherGroup), nil, true, ": Uid"},
		"own-identity":     {self(), nil, true, ""},
		"no-arguments":     {nil, nil, true, "arguments"},
	} {
		out, diagnostic, err := child(t, c.args, c.extra, c.setsid, request)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || out != "" || !strings.Contains(diagnostic, "clean worker refused") {
			t.Fatalf("%s: worker admitted: %v %q %q", name, err, out, diagnostic)
		}
		if c.reason != "" && !strings.Contains(diagnostic, c.reason) {
			t.Errorf("%s: refused for another reason: %q", name, diagnostic)
		}
	}
}

func put(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

// observe and delete run in-process here; the self-checks are tested above.
func TestObserveReportsEveryEntryOwnerAndDeleteLeavesOnlyTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "leaf")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".superbench-git", "objects"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(root, ".superbench-git", "HEAD"), "ref: refs/heads/main\n", 0600)
	put(t, filepath.Join(root, "file"), "content", 0400)
	if err := os.Symlink("file", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	reply, err := observe(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = reply.Valid(Observe); err != nil || len(reply.Owners) != 6 {
		t.Fatalf("observation: %+v %v", reply, err)
	}
	for _, o := range reply.Owners {
		info, err := os.Lstat(filepath.Join(root, o.Path))
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		if o.UID != st.Uid || o.GID != st.Gid || o.Nlink != uint64(st.Nlink) || o.Inode != st.Ino || o.Mode != uint32(info.Mode()) {
			t.Fatalf("owner of %s differs: %+v", o.Path, o)
		}
	}
	if raw, err := EncodeReply(reply, Observe); err != nil || len(raw) == 0 {
		t.Fatal("observation does not encode", err)
	}
	put(t, filepath.Join(root, "added"), "new", 0600)
	if err = reap.DeleteContents(root, *reply.Tree); err == nil {
		t.Fatal("an entry added after observation was deleted")
	}
	if err = os.Remove(filepath.Join(root, "added")); err != nil {
		t.Fatal(err)
	}
	if err = reap.DeleteContents(root, *reply.Tree); err != nil {
		t.Fatal(err)
	}
	after, err := observe(root)
	if err != nil || len(after.Tree.Entries) != 0 || after.Tree.Root != reply.Tree.Root {
		t.Fatalf("root not left empty and unchanged: %+v %v", after, err)
	}
}

// A file the worker cannot read, or a directory it cannot search, refuses the
// observation instead of producing a partial inventory.
func TestObserveRefusesUnreadableEntries(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("ordinary test requires an unprivileged user")
	}
	for name, mode := range map[string]os.FileMode{"file": 0200, "directory": os.ModeDir | 0600} {
		root := filepath.Join(t.TempDir(), "leaf")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "entry")
		if mode.IsDir() {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(path, "inside"), "x", 0600)
		} else {
			put(t, path, "x", 0600)
		}
		if err := os.Chmod(path, mode.Perm()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, 0700) })
		if _, err := observe(root); err == nil {
			t.Errorf("unreadable %s observed", name)
		}
	}
}
