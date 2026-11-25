package remotessh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

const (
	TestBench = "0123456789abcdef0123456789abcdef"
	TestLease = "fedcba9876543210fedcba9876543210"
	TestExec  = "00112233445566778899aabbccddeeff"
)

func WriterClaims() remoteca.Claims {
	return remoteca.Claims{BenchID: TestBench, LeaseID: TestLease, Generation: 3, ExecutionID: TestExec}
}

// Admitter is a fake lease: it admits exactly its claims until refused.
type Admitter struct {
	mu     sync.Mutex
	claims remoteca.Claims
	Root   string
	refuse error
	panics bool
	calls  int
}

func (a *Admitter) Admit(c remoteca.Claims) (remotestate.Admission, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.panics {
		panic("admit panics")
	}
	if a.refuse != nil {
		return remotestate.Admission{}, a.refuse
	}
	if c != a.claims {
		return remotestate.Admission{}, remotestate.ErrRevoked
	}
	return remotestate.Admission{Key: "card", BenchID: c.BenchID, LayoutID: c.BenchID, Holder: "worker", Root: a.Root, Claims: c, Execution: execution.Execution{ID: c.ExecutionID, Kind: execution.KindRemote, Phase: "running"}}, nil
}

func (a *Admitter) Set(err error)    { a.mu.Lock(); a.refuse = err; a.mu.Unlock() }
func (a *Admitter) SetPanics(v bool) { a.mu.Lock(); a.panics = v; a.mu.Unlock() }
func (a *Admitter) Count() int       { a.mu.Lock(); defer a.mu.Unlock(); return a.calls }

// AuditLog is an in-memory Audit with EndSession's first-reason rule.
type AuditLog struct {
	mu   sync.Mutex
	rows []remoteschema.SessionRecord
}

func (f *AuditLog) SessionStarted(a remotestate.Admission, r remoteschema.SessionRecord) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.Key, r.BenchID = a.Key, a.BenchID
	f.rows = append(f.rows, r)
	r.ID = int64(len(f.rows))
	f.rows[len(f.rows)-1].ID = r.ID
	return r.ID, nil
}

func (f *AuditLog) SessionEnded(id int64, in, out uint64, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id < 1 || int(id) > len(f.rows) {
		return errors.New("unknown session")
	}
	r := &f.rows[id-1]
	r.BytesIn, r.BytesOut = max(r.BytesIn, in), max(r.BytesOut, out)
	if r.EndReason == "" {
		r.EndReason = reason
	}
	return nil
}

func (f *AuditLog) Snapshot() []remoteschema.SessionRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.rows)
}

// WaitReasons waits until every row has ended with want.
func (f *AuditLog) WaitReasons(t *testing.T, n int, want string, within time.Duration) []remoteschema.SessionRecord {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		rows := f.Snapshot()
		ok := len(rows) == n
		for _, r := range rows {
			ok = ok && r.EndReason == want
		}
		if ok {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit rows %+v, want %d ended %q", rows, n, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Launches records every Start; with no inner launcher it starts a fake
// process that runs until terminated.
type Launches struct {
	inner Launcher
	mu    sync.Mutex
	specs []SessionSpec
	procs []*fakeProcess
}

func (l *Launches) Start(ctx context.Context, a remotestate.Admission, spec SessionSpec, fds []*os.File) (Process, error) {
	l.mu.Lock()
	l.specs = append(l.specs, spec)
	l.mu.Unlock()
	if l.inner != nil {
		return l.inner.Start(ctx, a, spec, fds)
	}
	closeFiles(fds)
	p := &fakeProcess{done: make(chan struct{})}
	l.mu.Lock()
	l.procs = append(l.procs, p)
	l.mu.Unlock()
	return p, nil
}

// Procs are the fake processes started so far.
func (l *Launches) Procs() []*fakeProcess {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.procs)
}

func (l *Launches) Started() []SessionSpec {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.specs)
}

type fakeProcess struct {
	done chan struct{}
	once sync.Once
}

func (p *fakeProcess) Wait() (int, string, error) { <-p.done; return 0, "TERM", nil }

// Exited is closed once the fake process was terminated.
func (p *fakeProcess) Exited() <-chan struct{} { return p.done }
func (p *fakeProcess) Terminate()              { p.once.Do(func() { close(p.done) }) }

// Clock is a settable offset from the real clock: deadlines are real timers.
type Clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *Clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return time.Now().Add(c.offset) }
func (c *Clock) Set(d time.Duration) {
	c.mu.Lock()
	c.offset = d
	c.mu.Unlock()
}

type Harness struct {
	Auth     *remoteca.Authority
	Lease    *Admitter
	Log      *AuditLog
	Launches *Launches
	Reg      *Registry
	Clock    *Clock
	Cfg      Config
	Key      ssh.Signer // the client's key
	Issued   remoteca.Issued
}

func NewHarness(t testing.TB, inner Launcher) *Harness {
	t.Helper()
	auth, err := remoteca.Open(filepath.Join(t.TempDir(), "remote"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Harness{Auth: auth, Lease: &Admitter{claims: WriterClaims(), Root: t.TempDir()}, Log: &AuditLog{}, Launches: &Launches{inner: inner}, Reg: NewRegistry(), Clock: &Clock{}}
	h.Cfg = Config{Authority: auth, Admit: h.Lease.Admit, Launcher: h.Launches, Audit: h.Log, Registry: h.Reg, Now: h.Clock.now}
	h.Key = NewKey(t)
	h.Issued = Issue(t, auth, h.Key, WriterClaims(), time.Now())
	before := panics.Load()
	t.Cleanup(func() {
		if n := panics.Load() - before; n != 0 {
			t.Errorf("%d panics recovered", n)
		}
	})
	return h
}

func NewKey(t testing.TB) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func Issue(t testing.TB, auth *remoteca.Authority, key ssh.Signer, c remoteca.Claims, now time.Time) remoteca.Issued {
	t.Helper()
	issued, err := auth.IssueWriter(string(ssh.MarshalAuthorizedKey(key.PublicKey())), c, now)
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

// CertSigner is key with the issued certificate, as a client presents it.
func CertSigner(t testing.TB, key ssh.Signer, issued remoteca.Issued) ssh.Signer {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(issued.Line))
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewCertSigner(k.(*ssh.Certificate), key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *Harness) ClientConfig(user string, methods ...ssh.AuthMethod) *ssh.ClientConfig {
	return &ssh.ClientConfig{User: user, Auth: methods, HostKeyCallback: ssh.FixedHostKey(h.Auth.HostSigner().PublicKey()), Timeout: 5 * time.Second}
}

func (h *Harness) CertAuth(t testing.TB) ssh.AuthMethod {
	return ssh.PublicKeys(CertSigner(t, h.Key, h.Issued))
}

// Pipe serves one in-memory connection with HandleConn (no sweep) and
// returns the client's end and a channel closed when HandleConn returns.
func (h *Harness) Pipe(t *testing.T) (net.Conn, <-chan struct{}) {
	t.Helper()
	server, client := socketPair(t)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(done)
		HandleConn(ctx, server, h.Cfg)
	}()
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})
	return client, done
}

// DialPipe authenticates over a pipe.
func (h *Harness) DialPipe(t *testing.T, user string, methods ...ssh.AuthMethod) (*ssh.Client, <-chan struct{}, error) {
	t.Helper()
	nc, done := h.Pipe(t)
	c, chans, reqs, err := ssh.NewClientConn(nc, "pipe", h.ClientConfig(user, methods...))
	if err != nil {
		return nil, done, err
	}
	client := ssh.NewClient(c, chans, reqs)
	t.Cleanup(func() { _ = client.Close() })
	return client, done, nil
}

func (h *Harness) Connect(t *testing.T) (*ssh.Client, <-chan struct{}) {
	t.Helper()
	c, done, err := h.DialPipe(t, remoteca.Principal(TestBench), h.CertAuth(t))
	if err != nil {
		t.Fatal(err)
	}
	return c, done
}

// ServeTCP runs Serve on a loopback listener until the test ends.
func (h *Harness) ServeTCP(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, h.Cfg) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return l.Addr().String()
}

func (h *Harness) DialTCP(t *testing.T, addr string, signer ssh.Signer) *ssh.Client {
	t.Helper()
	c, err := ssh.Dial("tcp", addr, h.ClientConfig(remoteca.Principal(TestBench), ssh.PublicKeys(signer)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// WaitRegistered waits until the registry holds n connections. The client
// sees authentication succeed before the server registers the connection,
// and the server forgets it only after its sessions ended, so neither
// Connect nor a closed client says the registry has caught up.
func (h *Harness) WaitRegistered(t testing.TB, n int, within time.Duration) []*conn {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if x := h.Reg.snapshot(); len(x) == n {
			return x
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections registered, want %d", h.Reg.Len(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// ClosedWithin reports whether the client's connection ended in time.
func ClosedWithin(c *ssh.Client, d time.Duration) bool {
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// socketPair is a connected stream pair. net.Pipe is unbuffered, and both
// SSH ends write their version line before reading.
func socketPair(t testing.TB) (net.Conn, net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	var conns [2]net.Conn
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "socketpair")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c
	}
	return conns[0], conns[1]
}

// mismatched presents a certificate but signs with another key.
type mismatched struct {
	cert ssh.PublicKey
	key  ssh.Signer
}

func (m mismatched) PublicKey() ssh.PublicKey { return m.cert }
func (m mismatched) Sign(r io.Reader, data []byte) (*ssh.Signature, error) {
	return m.key.Sign(r, data)
}

func foreignKeySigner(t testing.TB, issued remoteca.Issued) ssh.Signer {
	t.Helper()
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(issued.Line))
	if err != nil {
		t.Fatal(err)
	}
	return mismatched{cert: k, key: NewKey(t)}
}
