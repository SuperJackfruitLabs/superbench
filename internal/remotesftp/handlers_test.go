package remotesftp

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// serve starts a server over a temporary workspace root and returns a client
// for it, the workspace directory and the directory that holds it. The holder
// is "outside": nothing a client does may change it.
func serve(t *testing.T) (*sftp.Client, string, string) {
	t.Helper()
	outside := t.TempDir()
	workspace := filepath.Join(outside, "workspace")
	for _, dir := range []string{workspace, filepath.Join(workspace, "alpha"), filepath.Join(workspace, "beta")} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(server, root) }()
	c, err := sftp.NewClientPipe(client, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve after a clean close: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the client closed")
		}
		root.Close()
	})
	return c, workspace, outside
}

// snapshot records every path and file content under dir, workspace excluded.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	seen := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == filepath.Join(dir, "workspace") {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			value += ":" + string(raw)
		}
		seen[p] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seen
}

func sameSnapshot(t *testing.T, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("outside the root changed: %v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("outside the root changed at %s: %q -> %q", k, v, after[k])
		}
	}
}

// refused reports a server reply of no such file or permission denied; the
// client turns those two statuses into fs.ErrNotExist and fs.ErrPermission.
func refused(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("%s: %v, want no such file or permission denied", what, err)
	}
}

func denied(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("%s: %v, want permission denied", what, err)
	}
}

func TestSFTPRootedAtWorkspace(t *testing.T) {
	c, workspace, _ := serve(t)
	wd, err := c.Getwd()
	if err != nil || wd != "/" {
		t.Fatalf("Getwd() = %q, %v; want /", wd, err)
	}
	if real, err := c.RealPath("."); err != nil || real != "/" {
		t.Fatalf("RealPath(.) = %q, %v; want /", real, err)
	}
	entries, err := c.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Fatalf("ReadDir(/) = %v, want [alpha beta]", names)
	}
	f, err := c.Create("/alpha/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello bench")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Rename("alpha/notes.txt", "/beta/moved.txt"); err != nil {
		t.Fatal(err)
	}
	r, err := c.Open("beta/moved.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(raw) != "hello bench" {
		t.Fatalf("read back %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, "beta", "moved.txt")); err != nil || string(raw) != "hello bench" {
		t.Fatalf("on disk %q, %v", raw, err)
	}
	// The rest of the allowed set: mkdir, chmod, chtimes, truncate, symlink
	// inside the root, readlink, remove and rmdir.
	if err := c.Mkdir("/alpha/sub"); err != nil {
		t.Fatal(err)
	}
	if err := c.Chmod("/beta/moved.txt", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Truncate("/beta/moved.txt", 5); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0)
	if err := c.Chtimes("/beta/moved.txt", when, when); err != nil {
		t.Fatal(err)
	}
	info, err := c.Stat("/beta/moved.txt")
	if err != nil || info.Size() != 5 || info.Mode().Perm() != 0o600 || !info.ModTime().Equal(when) {
		t.Fatalf("Stat after setstat = %v, %v", info, err)
	}
	if err := c.Symlink("moved.txt", "/beta/link"); err != nil {
		t.Fatal(err)
	}
	if target, err := c.ReadLink("/beta/link"); err != nil || target != "moved.txt" {
		t.Fatalf("ReadLink = %q, %v", target, err)
	}
	if info, err := c.Stat("/beta/link"); err != nil || info.Size() != 5 {
		t.Fatalf("Stat through an inside link = %v, %v", info, err)
	}
	if err := c.Remove("/beta/link"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveDirectory("/alpha/sub"); err != nil {
		t.Fatal(err)
	}
}

func TestSFTPRefusesEscapes(t *testing.T) {
	c, _, outside := serve(t)
	before := snapshot(t, outside)
	for _, p := range []string{"../secret", "/../secret", "/../../etc/passwd", "alpha/../../secret", "../x"} {
		_, err := c.Open(p)
		refused(t, "Open("+p+")", err)
	}
	// A write aimed outside lands, at most, inside the root.
	for _, p := range []string{"../x", "/../../x", "alpha/../../x"} {
		f, err := c.Create(p)
		if err == nil {
			f.Close()
		}
	}
	if err := c.Mkdir("../escaped"); err != nil && !isStatus(err) {
		t.Fatal(err)
	}
	_ = c.Rename("alpha", "../stolen")
	_ = c.Remove("../secret")
	sameSnapshot(t, before, snapshot(t, outside))
}

// isStatus reports an SFTP status reply, as opposed to a broken connection.
func isStatus(err error) bool {
	var status *sftp.StatusError
	return errors.As(err, &status) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}

func TestSFTPRefusesSymlinkEscape(t *testing.T) {
	c, workspace, outside := serve(t)
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, outside)
	_, err := c.Open("link/secret")
	refused(t, "Open(link/secret)", err)
	_, err = c.Stat("link/secret")
	refused(t, "Stat(link/secret)", err)
	_, err = c.ReadDir("link")
	refused(t, "ReadDir(link)", err)
	_, err = c.Create("link/planted")
	refused(t, "Create(link/planted)", err)
	refused(t, "Remove(link/secret)", c.Remove("link/secret"))
	refused(t, "Chmod(link/secret)", c.Chmod("link/secret", 0o777))
	refused(t, "Rename(link/secret)", c.Rename("link/secret", "alpha/taken"))

	// A client-made link stores its target verbatim and is never followed out.
	if err := c.Symlink("/etc", "l2"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(workspace, "l2")); err != nil || target != "/etc" {
		t.Fatalf("stored target %q, %v; want /etc verbatim", target, err)
	}
	if target, err := c.ReadLink("l2"); err != nil || target != "/etc" {
		t.Fatalf("ReadLink(l2) = %q, %v", target, err)
	}
	_, err = c.Open("l2/passwd")
	refused(t, "Open(l2/passwd)", err)
	if err := c.Symlink("../secret", "alpha/up"); err != nil {
		t.Fatal(err)
	}
	_, err = c.Open("alpha/up")
	refused(t, "Open(alpha/up)", err)
	sameSnapshot(t, before, snapshot(t, outside))
}

func TestSFTPRefusesChownAndLink(t *testing.T) {
	c, workspace, _ := serve(t)
	if err := os.WriteFile(filepath.Join(workspace, "alpha", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	denied(t, "Chown", c.Chown("alpha/f", os.Getuid(), os.Getgid()))
	denied(t, "Link", c.Link("alpha/f", "alpha/hard"))
	if _, err := os.Lstat(filepath.Join(workspace, "alpha", "hard")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hard link was made: %v", err)
	}
	// uid and gid are refused even when another attribute rides along.
	f, err := c.OpenFile("alpha/f", os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	denied(t, "Fchown", f.Chown(os.Getuid(), os.Getgid()))
	f.Close()
}

func TestSFTPWriteFailureKeepsServing(t *testing.T) {
	c, workspace, _ := serve(t)
	if err := os.WriteFile(filepath.Join(workspace, "plain"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A parent that is a file fails the write for every caller, root included.
	_, err := c.Create("plain/child")
	if err == nil || !isStatus(err) {
		t.Fatalf("Create under a file = %v, want an SFTP failure status", err)
	}
	if _, err := c.Stat("alpha"); err != nil {
		t.Fatalf("Stat after a failed write: %v", err)
	}
	if os.Getuid() == 0 {
		t.Log("root ignores directory modes; the 0500 case needs an unprivileged user")
		return
	}
	locked := filepath.Join(workspace, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	_, err = c.Create("locked/f")
	if err == nil || !isStatus(err) {
		t.Fatalf("Create in a 0500 directory = %v, want an SFTP failure status", err)
	}
	if err := c.Mkdir("locked/d"); err == nil || !isStatus(err) {
		t.Fatalf("Mkdir in a 0500 directory = %v, want an SFTP failure status", err)
	}
	if info, err := c.Stat("locked"); err != nil || !info.IsDir() {
		t.Fatalf("Stat after a failed write = %v, %v", info, err)
	}
}

func TestRefuseRoot(t *testing.T) {
	if RefuseRoot(0) == nil {
		t.Fatal("RefuseRoot(0) = nil, want an error")
	}
	if err := RefuseRoot(1000); err != nil {
		t.Fatalf("RefuseRoot(1000) = %v", err)
	}
}
