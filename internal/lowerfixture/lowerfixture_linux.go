//go:build linux && freshstoragefixture

// Package lowerfixture holds the shared helpers of the privileged lower storage
// fixtures in payloadbootstrap. It builds only into binaries tagged
// freshstoragefixture and is never part of an installed executable.
package lowerfixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

func Hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func Info(t *testing.T, path string) os.FileInfo {
	t.Helper()
	s, e := os.Lstat(path)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func AssertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(got, want) {
		t.Fatalf("retained evidence differs: %s %v", path, e)
	}
}
func VerifyHelper(t *testing.T, path, want string) {
	t.Helper()
	if len(want) != 64 || storagecustody.TrustedAncestors(path) != nil {
		t.Fatal("helper custody missing")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0755 || st.Uid != 0 || st.Nlink != 1 || before.Size() > 128<<20 {
		t.Fatal("helper custody invalid")
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, (128<<20)+1)); err != nil {
		t.Fatal(err)
	}
	after, err := f.Stat()
	named, e := os.Lstat(path)
	if err != nil || e != nil || !os.SameFile(before, after) || !os.SameFile(before, named) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || hex.EncodeToString(h.Sum(nil)) != want {
		t.Fatal("helper pin differs")
	}
}
func DaemonGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return DaemonGitInput(t, dir, "", args...)
}
func DaemonGitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 61001, Gid: 61003, Groups: []uint32{}}}
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("owned source Git failed: %v %s", e, out)
	}
	return string(out)
}
