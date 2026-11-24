package remotessh_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh/remotesshtest"
)

// TestStockOpenSSHClient drives the server with the stock ssh, scp and sftp
// binaries, configured only by flags, as a remote agent would.
func TestStockOpenSSHClient(t *testing.T) {
	for _, bin := range []string{"ssh", "scp", "sftp"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("CI") == "true" {
				t.Fatalf("%s is required in CI: %v", bin, err)
			}
			t.Skipf("%s not installed", bin)
		}
	}
	h := remotessh.NewHarness(t, remotesshtest.Local{})
	root := h.Lease.Root
	if err := os.Mkdir(filepath.Join(root, "member"), 0700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	key, cert, kh := filepath.Join(dir, "key"), filepath.Join(dir, "key-cert.pub"), filepath.Join(dir, "known_hosts")
	issued := remotessh.Issue(t, h.Auth, signer, remotessh.WriterClaims(), time.Now())
	write(t, key, string(pem.EncodeToMemory(block)))
	write(t, cert, issued.Line+"\n")
	addr := h.ServeTCP(t)
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	write(t, kh, "[127.0.0.1]:"+port+" "+h.Auth.HostKeyLine()+"\n")
	target := remoteca.Principal(remotessh.TestBench) + "@127.0.0.1"
	options := func(knownHosts string) []string {
		return []string{"-F", "/dev/null", "-i", key, "-o", "CertificateFile=" + cert, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "UserKnownHostsFile=" + knownHosts, "-o", "StrictHostKeyChecking=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
	}
	run := func(bin string, args ...string) (string, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env = dir, []string{"HOME=" + dir, "PATH=" + os.Getenv("PATH"), "LANG=C"}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			t.Fatalf("%s %v timed out: %s", bin, args, stderr.String())
		}
		return stdout.String(), stderr.String(), err
	}
	ssh := func(args ...string) []string {
		return append(append(options(kh), "-p", port), append([]string{target}, args...)...)
	}

	if out, errOut, err := run("ssh", ssh("echo hi; pwd")...); err != nil || out != "hi\n"+root+"\n" {
		t.Fatalf("exec: %q %v %s", out, err, errOut)
	}
	if out, errOut, err := run("ssh", append([]string{"-tt"}, ssh("tty")...)...); err != nil || !regexp.MustCompile(`^/dev/pts/\d+`).MatchString(out) {
		t.Fatalf("pty: %q %v %s", out, err, errOut)
	}

	write(t, filepath.Join(dir, "local"), "uploaded\n")
	write(t, filepath.Join(dir, "batch"), "put local up\nget up down\nls\ncd ..\npwd\ncd /..\npwd\n")
	sftpArgs := append(append([]string{"-b", filepath.Join(dir, "batch")}, options(kh)...), "-P", port, target)
	out, errOut, err := run("sftp", sftpArgs...)
	if err != nil {
		t.Fatalf("sftp: %v %s %s", err, out, errOut)
	}
	if got := read(t, filepath.Join(root, "up")); got != "uploaded\n" {
		t.Fatalf("uploaded %q", got)
	}
	if got := read(t, filepath.Join(dir, "down")); got != "uploaded\n" {
		t.Fatalf("downloaded %q", got)
	}
	if n := strings.Count(out, "Remote working directory: /\n"); n != 2 || !strings.Contains(out, "member") {
		t.Fatalf("sftp output %q", out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "up")); err == nil {
		t.Fatal("sftp wrote outside the root")
	}

	scpArgs := append(append([]string{}, options(kh)...), "-P", port, filepath.Join(dir, "local"), target+":member/f")
	if out, errOut, err := run("scp", scpArgs...); err != nil {
		t.Fatalf("scp: %v %s %s", err, out, errOut)
	}
	if got := read(t, filepath.Join(root, "member", "f")); got != "uploaded\n" {
		t.Fatalf("scp copied %q", got)
	}

	before := len(h.Launches.Started())
	// "-L 0:..." is refused by ssh itself; a real local forward is refused
	// by the server when a connection arrives.
	lport := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	forward := exec.CommandContext(ctx, "ssh", append([]string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", lport + ":127.0.0.1:9"}, ssh()...)...)
	var forwardErr bytes.Buffer
	forward.Dir, forward.Env, forward.Stderr = dir, []string{"HOME=" + dir, "PATH=" + os.Getenv("PATH")}, &forwardErr
	if err := forward.Start(); err != nil {
		t.Fatal(err)
	}
	var local net.Conn
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if local, err = net.Dial("tcp", "127.0.0.1:"+lport); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("local forward never listened: %s", forwardErr.String())
		}
	}
	_ = local.SetReadDeadline(time.Now().Add(10 * time.Second))
	if n, err := local.Read(make([]byte, 1)); n != 0 || err == nil || os.IsTimeout(err) {
		t.Fatalf("forwarded connection read %d bytes, %v", n, err)
	}
	_ = local.Close()
	cancel()
	_ = forward.Wait()
	if !strings.Contains(forwardErr.String(), "administratively prohibited") {
		t.Errorf("local forward: %q", forwardErr.String())
	}
	for name, c := range map[string]struct {
		args    []string
		refusal string
	}{
		"stdio forward":  {append([]string{"-W", "127.0.0.1:9"}, ssh()...), "administratively prohibited"},
		"remote forward": {append([]string{"-N", "-o", "ExitOnForwardFailure=yes", "-R", "0:127.0.0.1:9"}, ssh()...), "remote port forwarding failed"},
	} {
		if _, errOut, err := run("ssh", c.args...); err == nil || !strings.Contains(errOut, c.refusal) {
			t.Errorf("%s: %v %q", name, err, errOut)
		}
	}
	if n := len(h.Launches.Started()) - before; n != 0 {
		t.Fatalf("Start called %d times for forwarding attempts", n)
	}

	other, err := remoteca.Open(filepath.Join(t.TempDir(), "remote"))
	if err != nil {
		t.Fatal(err)
	}
	wrong := filepath.Join(dir, "wrong_hosts")
	write(t, wrong, "[127.0.0.1]:"+port+" "+other.HostKeyLine()+"\n")
	before = len(h.Launches.Started())
	if _, _, err := run("ssh", append(append(options(wrong), "-p", port), target, "echo hi")...); err == nil {
		t.Fatal("connected with a wrong pinned host key")
	}
	if n := len(h.Launches.Started()) - before; n != 0 {
		t.Fatalf("Start called %d times", n)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, _ := net.SplitHostPort(l.Addr().String())
	return port
}
