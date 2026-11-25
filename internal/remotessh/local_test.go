package remotessh_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh/remotesshtest"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

func TestExecRunsAndAudits(t *testing.T) {
	h := remotessh.NewHarness(t, remotesshtest.Local{})
	c, _ := h.Connect(t)
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	s.Stdin, s.Stdout, s.Stderr = strings.NewReader("input\n"), &stdout, &stderr
	err = s.Run("read line; echo \"$line\"; pwd; echo oops >&2; exit 3")
	var exit *ssh.ExitError
	if !errors.As(err, &exit) || exit.ExitStatus() != 3 {
		t.Fatalf("run: %v", err)
	}
	if want := "input\n" + h.Lease.Root + "\n"; !samePath(stdout.String(), want) || stderr.String() != "oops\n" {
		t.Fatalf("stdout %q stderr %q", stdout.String(), stderr.String())
	}
	rows := h.Log.WaitReasons(t, 1, "exit", 5*time.Second)
	r := rows[0]
	if r.Kind != "exec" || r.CommandName != "read" || r.Serial != h.Issued.Serial || r.Fingerprint != h.Issued.Fingerprint || r.Key != "card" || r.BenchID != remotessh.TestBench || r.BytesIn != 6 || r.BytesOut != uint64(stdout.Len()+stderr.Len()) {
		t.Fatalf("audit %+v", r)
	}
	s2, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	err = s2.Run("kill -USR1 $$")
	if !errors.As(err, &exit) || exit.Signal() != "USR1" {
		t.Fatalf("signalled run: %v", err)
	}
}

// samePath compares output that ends in a directory, allowing for a temp
// directory reached through a symbolic link (macOS's /var -> /private/var).
func samePath(got, want string) bool {
	return got == want || strings.TrimPrefix(got, "input\n/private") == strings.TrimPrefix(want, "input\n")
}

// TestSweepClosesRevoked: a revoked lease closes the connection within the
// sweep, terminates its running command and ends the audit row "revoked".
func TestSweepClosesRevoked(t *testing.T) {
	h := remotessh.NewHarness(t, remotesshtest.Local{})
	addr := h.ServeTCP(t)
	c := h.DialTCP(t, addr, remotessh.CertSigner(t, h.Key, h.Issued))
	idle := h.DialTCP(t, addr, remotessh.CertSigner(t, h.Key, h.Issued))
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start("echo $$; exec sleep 60"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(line)
	}
	h.Lease.Set(remotestate.ErrRevoked)
	for _, x := range []*ssh.Client{c, idle} {
		if !remotessh.ClosedWithin(x, 2*time.Second) {
			t.Fatal("connection open after revocation")
		}
	}
	h.Log.WaitReasons(t, 1, "revoked", time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("sleep %d still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fixture is a ready v2 workspace held by "worker", as remotestate's own
// fixture builds it, with scopes that exist until fenced.
type fixture struct {
	m     remotestate.Manager
	token string
}

type scopes struct {
	mu      sync.Mutex
	present map[string]bool
}

func (s *scopes) Create(_, id string) (execution.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.present[id] = true
	return execution.Identity{BootID: "test-boot", HostID: "test-host"}, nil
}
func (s *scopes) Fence(e *execution.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.present, e.ID)
	return nil
}
func (s *scopes) Empty(e *execution.Execution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.present[e.ID] {
		return errors.New("scope still populated")
	}
	return nil
}
func (s *scopes) Present(e *execution.Execution) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.present[e.ID], nil
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := gitcontrol.Command(context.Background(), dir, args...)
	cmd.Env = append(gitcontrol.Environment(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git is required")
	}
	root := t.TempDir()
	store := bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}
	spec := workspace.Spec{Key: "card", CheckpointRepository: "api"}
	for _, name := range []string{"api", "web"} {
		repo := filepath.Join(root, name)
		if err := os.Mkdir(repo, 0700); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "init", "-b", "main")
		if err := os.WriteFile(filepath.Join(repo, "source.txt"), []byte(name+" base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, repo, "add", ".")
		git(t, repo, "commit", "-m", "base")
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: name, Repository: repo})
	}
	ctx := context.Background()
	if _, err := store.InitializeWorkspaceContext(ctx, "creation", spec); err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWorkspaceContext(ctx, "card", "worker")
	if err != nil {
		t.Fatal(err)
	}
	m, err := remotestate.ForStore(store, &scopes{present: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{m: m, token: held.Lease.Token}
}

// The lease token never reaches the client, the audit rows or a log: the
// server sees only the certificate's claims. A real lease, granted under
// its token, and the real admission and audit.
func TestNoLeaseTokenReachesClientAuditOrLog(t *testing.T) {
	f := newFixture(t)
	if len(f.token) != 32 {
		t.Fatalf("fixture token %d characters", len(f.token))
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	g, err := f.m.Grant(context.Background(), "card", f.token)
	if err != nil {
		t.Fatal(err)
	}
	h := remotessh.NewHarness(t, remotesshtest.Local{})
	h.Cfg.Admit, h.Cfg.Audit = f.m.Admit, f.m
	key := remotessh.NewKey(t)
	issued := remotessh.Issue(t, h.Auth, key, g.Claims, time.Now())
	addr := h.ServeTCP(t)
	var seen []string // everything the client was shown
	user := remoteca.Principal(g.Claims.BenchID)
	cfg := h.ClientConfig(user, ssh.PublicKeys(remotessh.CertSigner(t, key, issued)))
	cfg.BannerCallback = func(m string) error { seen = append(seen, m); return nil }
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	seen = append(seen, string(c.ServerVersion()))
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	s.Stdout, s.Stderr = &stdout, &stderr
	if err := s.Run("echo ok; env; ls -a"); err != nil {
		t.Fatalf("run: %v %s", err, stderr.String())
	}
	seen = append(seen, stdout.String(), stderr.String())
	// Refusals: another bench's user, a session after the lease is fenced.
	other := h.ClientConfig(remoteca.Principal(strings.Repeat("0", 32)), ssh.PublicKeys(remotessh.CertSigner(t, key, issued)))
	if _, err := ssh.Dial("tcp", addr, other); err == nil {
		t.Fatal("another bench's user authenticated")
	} else {
		seen = append(seen, err.Error())
	}
	long, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := long.Start("sleep 60"); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Fence(context.Background(), "card", f.token, false); err != nil {
		t.Fatal(err)
	}
	if !remotessh.ClosedWithin(c, 2*time.Second) {
		t.Fatal("connection open after the fence")
	}
	if _, err := ssh.Dial("tcp", addr, cfg); err == nil {
		t.Fatal("certificate admitted after the fence")
	} else {
		seen = append(seen, err.Error())
	}
	var status remotestate.Status
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err = f.m.Status("", 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Sessions) == 2 && status.Sessions[0].EndReason != "" && status.Sessions[1].EndReason != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions %+v", status.Sessions)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.Sessions[0].EndReason != "fenced" || status.Sessions[1].EndReason != "exit" || status.Sessions[1].Kind != "exec" || status.Sessions[1].CommandName != "echo" {
		t.Fatalf("sessions %+v", status.Sessions)
	}
	seen = append(seen, fmt.Sprintf("%+v", status), logged.String())
	for _, s := range seen {
		if strings.Contains(s, f.token) {
			t.Fatalf("lease token reached %q", s)
		}
	}
	if !strings.HasPrefix(stdout.String(), "ok\n") {
		t.Fatalf("stdout %q", stdout.String())
	}
	// The token-bearing record is still the state's own: the test can fail.
	var held string
	if err := f.m.Prep.Read(func(rows benchstate.Database) error {
		if b := rows["card"]; b != nil && b.Lease != nil {
			held = b.Lease.Token
		}
		return nil
	}); err != nil || held != f.token {
		t.Fatalf("state token %v", err)
	}
}
