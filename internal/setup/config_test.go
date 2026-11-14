package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func configFile(t *testing.T, raw string) string {
	t.Helper()
	root := t.TempDir()
	if e := os.Mkdir(filepath.Join(root, ".bench"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, ".bench/config.toml"), []byte(raw), 0644); e != nil {
		t.Fatal(e)
	}
	return root
}

func TestCommandsRespectOnceEachAndOffline(t *testing.T) {
	root := configFile(t, "[setup]\nonce = '''printf once; exit 0'''\neach = '''printf each:$BENCH_SETUP_OFFLINE'''\n")
	c, e := Read(root)
	if e != nil || c == nil || !c.Hooks() {
		t.Fatal(c, e)
	}
	for _, done := range []bool{false, true} {
		args := c.Command(done, true)
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = root
		out, e := cmd.CombinedOutput()
		want := "onceeach:1"
		if done {
			want = "each:1"
		}
		if e != nil || string(out) != want {
			t.Fatal(string(out), e)
		}
	}
	if e = Verify(root, c.Digest); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, ".bench/config.toml"), []byte("[setup]\neach='false'"), 0644); e != nil {
		t.Fatal(e)
	}
	if e = Verify(root, c.Digest); e == nil {
		t.Fatal("changed configuration retained once authority")
	}
}

func TestRejectInvalidConfigurationWithoutLeakingCommands(t *testing.T) {
	for _, raw := range []string{
		"[setup]\nonce='secret'\nonce='other'",
		"[setup]\nonce=3", "[setup]\nonse='secret'", "[quota]\nsize='0G'",
		"[setup]\nonce=\"secret\\u0000\"", "[setup]\nonce='" + strings.Repeat("x", 16385) + "'",
		strings.Repeat("# comment\n", MaxConfig), string([]byte{0xff}),
	} {
		if _, e := Read(configFile(t, raw)); e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatal("invalid configuration accepted or exposed", e)
		}
	}
}

func TestRefuseSpecialAndLinkedConfig(t *testing.T) {
	for _, kind := range []string{"file-link", "directory-link", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := configFile(t, "[setup]\nonce='true'")
			path := filepath.Join(root, ".bench/config.toml")
			if e := os.Remove(path); e != nil {
				t.Fatal(e)
			}
			var e error
			switch kind {
			case "file-link":
				e = os.Symlink("/dev/null", path)
			case "directory-link":
				if e = os.Remove(filepath.Join(root, ".bench")); e == nil {
					e = os.Symlink(t.TempDir(), filepath.Join(root, ".bench"))
				}
			case "fifo":
				e = syscall.Mkfifo(path, 0600)
			}
			if e != nil {
				t.Fatal(e)
			}
			if _, e = Read(root); e == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	if c, e := Read(t.TempDir()); e != nil || c != nil {
		t.Fatal("missing config no longer permits frozen setup", c, e)
	}
}
