package remoteaccess

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteproto"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerenew"
)

func requireSSH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatalf("ssh is required in CI: %v", err)
		}
		t.Skipf("ssh not installed: %v", err)
	}
}

// stock is a stock OpenSSH client configured only by flags, as a remote
// agent runs it: one key, one certificate, a pinned host key.
type stock struct {
	dir, key, cert, known, host, port string
}

func (f *fixture) stock(t *testing.T, c client, cred remoteproto.Credential) *stock {
	t.Helper()
	dir := t.TempDir()
	host, port, err := net.SplitHostPort(cred.Address)
	if err != nil {
		t.Fatal(err)
	}
	s := &stock{dir: dir, key: filepath.Join(dir, "key"), cert: filepath.Join(dir, "key-cert.pub"), known: filepath.Join(dir, "known_hosts"), host: host, port: port}
	for path, body := range map[string]string{s.key: string(c.pem), s.cert: cred.Certificate + "\n", s.known: "[" + host + "]:" + port + " " + cred.HostKey + "\n"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// certificate replaces the certificate the client presents.
func (s *stock) certificate(t *testing.T, line string) {
	t.Helper()
	if err := os.WriteFile(s.cert, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func (s *stock) command(ctx context.Context, user string, extra []string, remote ...string) *exec.Cmd {
	args := []string{"-F", "/dev/null", "-i", s.key, "-o", "CertificateFile=" + s.cert, "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "UserKnownHostsFile=" + s.known, "-o", "StrictHostKeyChecking=yes", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-p", s.port}
	args = append(append(args, extra...), user+"@"+s.host)
	cmd := exec.CommandContext(ctx, "ssh", append(args, remote...)...)
	cmd.Dir, cmd.Env = s.dir, []string{"HOME=" + s.dir, "PATH=" + os.Getenv("PATH"), "LANG=C"}
	return cmd
}

// run runs one remote command to completion.
func (s *stock) run(t *testing.T, user string, extra []string, remote ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := s.command(ctx, user, extra, remote...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("ssh %v timed out: %s", remote, stderr.String())
	}
	return stdout.String(), stderr.String(), err
}

// refused requires the certificate to be refused: ssh exits 255 with a
// permission refusal and the command never ran.
func (s *stock) refused(t *testing.T, user, why string) string {
	t.Helper()
	out, errOut, err := s.run(t, user, []string{"-v"}, "echo ran")
	if err == nil || strings.Contains(out, "ran") || !strings.Contains(errOut, "Permission denied") {
		t.Fatalf("%s: certificate not refused: %v %q %s", why, err, out, errOut)
	}
	return errOut
}

// live is a running ssh client: what it printed first, and its stderr.
type live struct {
	cmd    *exec.Cmd
	first  string
	done   chan struct{}
	err    error
	stderr lockedBuffer
	exited time.Time
}

// start runs ssh in the background. With a remote command that prints one
// line and keeps running, it returns once the line arrived, so the session
// is live. With none (ssh -N) it runs ssh with -v and returns once the
// client reports it authenticated, so the connection is open and idle.
func (s *stock) start(t *testing.T, user string, extra []string, remote ...string) *live {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	l := &live{done: make(chan struct{})}
	idle := len(remote) == 0
	if idle {
		extra = append([]string{"-N", "-v"}, extra...)
	}
	l.cmd = s.command(ctx, user, extra, remote...)
	out, err := l.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	errPipe, err := l.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-l.done })
	first, authenticated := make(chan string, 1), make(chan struct{})
	var copied sync.WaitGroup
	copied.Add(2)
	go func() {
		defer copied.Done()
		lines := bufio.NewReader(out)
		line, _ := lines.ReadString('\n')
		first <- strings.TrimSpace(line)
		_, _ = io.Copy(io.Discard, lines)
	}()
	go func() {
		defer copied.Done()
		lines, once := bufio.NewScanner(errPipe), sync.Once{}
		for lines.Scan() {
			_, _ = l.stderr.Write(append(lines.Bytes(), '\n'))
			if strings.Contains(lines.Text(), "Authenticated to") {
				once.Do(func() { close(authenticated) })
			}
		}
		_, _ = io.Copy(io.Discard, errPipe)
	}()
	go func() {
		copied.Wait()
		l.err = l.cmd.Wait()
		l.exited = time.Now()
		close(l.done)
	}()
	printed, ready := first, (<-chan struct{})(authenticated)
	if idle {
		printed = nil // ssh -N prints nothing; it may close its stdout at once
	} else {
		ready = nil
	}
	select {
	case l.first = <-printed:
		return l
	case <-ready:
		return l
	case <-time.After(30 * time.Second):
	}
	t.Fatalf("ssh %v never became live: %s", remote, l.stderr.String())
	return nil
}

// exitedBy waits for the client to exit and requires it by the deadline.
func (l *live) exitedBy(t *testing.T, deadline time.Time, why string) {
	t.Helper()
	select {
	case <-l.done:
	case <-time.After(time.Until(deadline) + 5*time.Second):
		t.Fatalf("%s: ssh still running", why)
	}
	if l.exited.After(deadline) {
		t.Fatalf("%s: ssh exited %v after the deadline", why, l.exited.Sub(deadline))
	}
}

func (l *live) running() bool {
	select {
	case <-l.done:
		return false
	default:
		return true
	}
}

// alive reports whether pid exists and is not a zombie.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z"
}

func TestRemoteAccessEndToEnd(t *testing.T) {
	requireSSH(t)
	f := newFixture(t, "card")
	f.serve(t)
	c := newClient(t)
	cred, _ := f.credential(t, "card", c)
	if cred.Address != f.addr || !strings.HasPrefix(cred.User, "b-") || cred.Generation < 1 || cred.HostKeyFingerprint != ssh.FingerprintSHA256(f.access.authority.HostSigner().PublicKey()) {
		t.Fatalf("credential %+v", cred)
	}
	s := f.stock(t, c, cred)
	out, errOut, err := s.run(t, cred.User, nil, "echo ok")
	if err != nil || out != "ok\n" {
		t.Fatalf("ssh: %q %v %s", out, err, errOut)
	}
	rows := f.sessions(t, "card", 1, 5*time.Second)
	if rows[0].EndReason != "exit" || rows[0].Kind != "exec" || rows[0].CommandName != "echo" || rows[0].Fingerprint != ssh.FingerprintSHA256(c.signer.PublicKey()) {
		t.Fatalf("audit %+v", rows[0])
	}
}

func TestExecPwdIsCredentialWorkingDirectory(t *testing.T) {
	requireSSH(t)
	f := newFixture(t, "card")
	f.serve(t)
	c := newClient(t)
	cred, _ := f.credential(t, "card", c)
	if !filepath.IsAbs(cred.WorkingDirectory) {
		t.Fatalf("working_directory %q", cred.WorkingDirectory)
	}
	out, errOut, err := f.stock(t, c, cred).run(t, cred.User, nil, "pwd")
	if err != nil || strings.TrimSpace(out) != cred.WorkingDirectory {
		t.Fatalf("pwd %q, working_directory %q: %v %s", out, cred.WorkingDirectory, err, errOut)
	}
}

// cut runs a session, then op, and requires the session cut within two
// seconds of the fence, the audit row "fenced" and the certificate refused.
func cut(t *testing.T, op string) {
	requireSSH(t)
	f := newFixture(t, "card")
	f.serve(t)
	c := newClient(t)
	cred, _ := f.credential(t, "card", c)
	s := f.stock(t, c, cred)
	l := s.start(t, cred.User, nil, "echo started; exec sleep 30")
	if l.first != "started" {
		t.Fatalf("first line %q", l.first)
	}
	f.op(t, op, "card")
	fenced, at := f.scopes.fences()
	if len(fenced) != 1 {
		t.Fatalf("%s fenced %v", op, fenced)
	}
	l.exitedBy(t, at.Add(2*time.Second), op)
	if l.err == nil {
		t.Fatalf("%s: ssh exited zero", op)
	}
	rows := f.sessions(t, "card", 1, 5*time.Second)
	if rows[0].EndReason != "fenced" {
		t.Fatalf("%s: audit %+v", op, rows[0])
	}
	s.refused(t, cred.User, op)
}

func TestCheckpointCutsLiveSession(t *testing.T) { cut(t, "checkpoint") }
func TestReleaseCutsLiveSession(t *testing.T)    { cut(t, "release") }
func TestStopCutsLiveSession(t *testing.T)       { cut(t, "stop") }

// Review Focus 1: a lease rotated under an idle connection (no channel)
// closes it within the sweep, and the old generation's certificate stays
// refused even once the renewed lease has a live remote execution.
func TestSweepClosesConnectionAfterRenewAdmin(t *testing.T) {
	requireSSH(t)
	f := newFixture(t, "card")
	f.serve(t)
	c := newClient(t)
	cred, _ := f.credential(t, "card", c)
	s := f.stock(t, c, cred)
	l := s.start(t, cred.User, nil)
	if _, err := f.ops.Execute(context.Background(), protocolv2.Request{Version: protocolv2.Version, Operation: "stop_admin", Key: "card"}, operator); err != nil {
		t.Fatal(err)
	}
	repair, err := f.ops.Store.WorkspaceRepair()
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := workspacerenew.Manager{Prep: repair.Prep}.Renew("card", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, at := f.scopes.fences()
	l.exitedBy(t, at.Add(2*time.Second), "renew_admin")
	s.refused(t, cred.User, "old generation")
	f.tokens["card"] = renewed.Lease.Token
	next, _ := f.credential(t, "card", c)
	if next.Generation != cred.Generation+1 || next.LeaseID != cred.LeaseID {
		t.Fatalf("renewed credential %+v", next)
	}
	s.refused(t, cred.User, "old generation beside a live execution")
	s.certificate(t, next.Certificate)
	if out, errOut, err := s.run(t, next.User, nil, "echo ok"); err != nil || out != "ok\n" {
		t.Fatalf("renewed certificate: %q %v %s", out, err, errOut)
	}
}

// A certificate names one bench: it is refused as another bench's user,
// and refused for its own bench once the lease it names has gone.
func TestWrongBenchCertificateRefused(t *testing.T) {
	requireSSH(t)
	f := newFixture(t, "card", "other")
	f.serve(t)
	c := newClient(t)
	a, _ := f.credential(t, "card", c)
	b, _ := f.credential(t, "other", c)
	s := f.stock(t, c, a)
	if out, errOut, err := s.run(t, a.User, nil, "echo ok"); err != nil || out != "ok\n" {
		t.Fatalf("own bench: %q %v %s", out, err, errOut)
	}
	s.refused(t, b.User, "bench A's certificate as bench B's user")
	s.certificate(t, b.Certificate)
	s.refused(t, a.User, "bench B's certificate as bench A's user")
	s.certificate(t, a.Certificate)
	f.op(t, "release", "card")
	s.refused(t, a.User, "released bench")
	f.lease(t, "card")
	s.refused(t, a.User, "a new acquisition of the bench")
}

// A certificate past its validity is refused; the same claims, freshly
// signed, are admitted.
func TestExpiredCertificateRefused(t *testing.T) {
	requireSSH(t)
	f := newFixture(t, "card")
	f.serve(t)
	c := newClient(t)
	cred, _ := f.credential(t, "card", c)
	s := f.stock(t, c, cred)
	m, err := remotestate.ForStore(f.ops.Store, f.scopes)
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.Grant(context.Background(), "card", f.tokens["card"])
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.access.authority.IssueWriter(c.authorized, g.Claims, time.Now().Add(-40*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s.certificate(t, old.Line)
	s.refused(t, cred.User, "expired")
	fresh, err := f.access.authority.IssueWriter(c.authorized, g.Claims, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.certificate(t, fresh.Line)
	if out, errOut, err := s.run(t, cred.User, nil, "echo ok"); err != nil || out != "ok\n" {
		t.Fatalf("fresh certificate: %q %v %s", out, err, errOut)
	}
}

// Review Focus 2: the client vanishes mid-session. The audit row ends
// "closed"; a process the session detached outlives the disconnect, and
// the release's fence kills it.
func TestAbruptDisconnectThenReleaseFencesLeftovers(t *testing.T) {
	requireSSH(t)
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Fatalf("setsid is required: %v", err)
	}
	f := newFixture(t, "card")
	f.serve(t)
	c := newClient(t)
	cred, _ := f.credential(t, "card", c)
	s := f.stock(t, c, cred)
	// The leftover leaves the session's process group, as a detached job
	// can; only the remote scope still holds it.
	l := s.start(t, cred.User, nil, "setsid nohup sleep 60 </dev/null >/dev/null 2>&1 & echo $!; exec sleep 30")
	pid, err := strconv.Atoi(l.first)
	if err != nil || !alive(pid) {
		t.Fatalf("leftover %q %v", l.first, err)
	}
	f.scopes.place(f.execution(t, "card"), pid)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if err := l.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	rows := f.sessions(t, "card", 1, 5*time.Second)
	if rows[0].EndReason != "closed" {
		t.Fatalf("audit %+v", rows[0])
	}
	time.Sleep(1500 * time.Millisecond) // past a sweep: nothing else kills it
	if !alive(pid) {
		t.Fatal("the leftover died before the fence; the test proves nothing")
	}
	f.op(t, "release", "card")
	deadline := time.Now().Add(2 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("leftover %d outlived the release", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fenced, _ := f.scopes.fences(); len(fenced) != 1 {
		t.Fatalf("fences %v", fenced)
	}
}

// Renewal over the socket extends the open connection past the first
// certificate's end; a connection whose key was not renewed closes then.
func TestRenewalOverSocketKeepsConnection(t *testing.T) {
	requireSSH(t)
	f := newFixture(t, "card")
	var shift atomic.Int64 // the certificate clock's offset from now
	f.access.now = func() time.Time { return time.Now().Add(time.Duration(shift.Load())) }
	f.serve(t)
	renewedKey, otherKey := newClient(t), newClient(t)
	shift.Store(int64(-30*time.Minute + 4*time.Second))
	first, _ := f.credential(t, "card", renewedKey)
	other, _ := f.credential(t, "card", otherKey)
	until, err := time.Parse(time.RFC3339Nano, first.ValidBefore)
	if err != nil || time.Until(until) > 5*time.Second || time.Until(until) < time.Second {
		t.Fatalf("short certificate valid before %s (%v)", first.ValidBefore, err)
	}
	kept := f.stock(t, renewedKey, first).start(t, first.User, nil)
	lapsed := f.stock(t, otherKey, other).start(t, other.User, nil)
	// The client reports authentication before the server registers the
	// connection, and a renewal extends only registered connections.
	for deadline := time.Now().Add(5 * time.Second); f.access.registry.Len() != 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d connections registered, want 2", f.access.registry.Len())
		}
	}
	shift.Store(0)
	if second, _ := f.credential(t, "card", renewedKey); second.LeaseID != first.LeaseID || second.Generation != first.Generation {
		t.Fatalf("renewal %+v", second)
	}
	lapsed.exitedBy(t, until.Add(2*time.Second), "unrenewed key")
	time.Sleep(time.Until(until.Add(2 * time.Second)))
	if !kept.running() {
		t.Fatalf("renewed connection closed: %v %s", kept.err, kept.stderr.String())
	}
}

// With a real lease token, end to end: the token never appears in a
// certificate, a log line, an audit row, a socket reply, the SSH banner or
// version, a client's output or a refusal.
func TestLeaseTokenNeverLeavesTheState(t *testing.T) {
	requireSSH(t)
	var logged lockedBuffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	f := newFixture(t, "card", "other")
	token := f.tokens["card"]
	if len(token) != 32 {
		t.Fatalf("fixture token %d characters", len(token))
	}
	f.serve(t)
	c := newClient(t)
	cred, reply := f.credential(t, "card", c)
	seen := []string{string(reply), cred.Certificate, logged.String()}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cred.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	cert := parsed.(*ssh.Certificate)
	seen = append(seen, cert.KeyId, strings.Join(cert.ValidPrincipals, ","), fmt.Sprint(cert.Permissions.Extensions), fmt.Sprint(cert.Permissions.CriticalOptions))
	s := f.stock(t, c, cred)
	out, errOut, err := s.run(t, cred.User, []string{"-v"}, "echo ok; env; ls -a")
	if err != nil || !strings.HasPrefix(out, "ok\n") || !strings.Contains(errOut, "SuperBench") {
		t.Fatalf("ssh: %q %v %s", out, err, errOut)
	}
	seen = append(seen, out, errOut)
	other, _ := f.credential(t, "other", c)
	seen = append(seen, s.refused(t, other.User, "another bench's user"))
	// A wrong token is refused, so the route compares the real one.
	if code, body := f.route(t, operator, "/v2/remote/credentials", credentialRequest("card", strings.Repeat("0", 32), c.authorized)); code != 403 {
		t.Fatalf("wrong token answered %d %s", code, body)
	} else {
		seen = append(seen, string(body))
	}
	l := s.start(t, cred.User, []string{"-v"}, "echo started; exec sleep 30")
	f.op(t, "release", "card")
	<-l.done
	seen = append(seen, l.stderr.String(), s.refused(t, cred.User, "after release"))
	f.sessions(t, "card", 2, 5*time.Second)
	_, status := f.status(t, "")
	seen = append(seen, string(status), logged.String())
	for _, x := range seen {
		if strings.Contains(x, token) {
			t.Fatalf("lease token reached %q", x)
		}
	}
	if !strings.Contains(string(status), `"fenced"`) || !strings.Contains(logged.String(), "remote access: listening") {
		t.Fatalf("the collection missed what it means to check: %s %s", status, logged.String())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
