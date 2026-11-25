package remotessh

import (
	"bufio"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

func TestCertificateAccepted(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	if v := string(c.ServerVersion()); v != ServerVersion {
		t.Fatalf("server version %q", v)
	}
	if n := h.Lease.Count(); n != 1 {
		t.Fatalf("Admit called %d times at authentication, want 1", n)
	}
	x := h.WaitRegistered(t, 1, 5*time.Second)
	if len(x) != 1 || x[0].claims != WriterClaims() || x[0].fingerprint != h.Issued.Fingerprint || x[0].serial != h.Issued.Serial || !x[0].deadline.Equal(h.Issued.ValidBefore) {
		t.Fatalf("registered %+v", x)
	}
}

func TestCertificateOnlyAuth(t *testing.T) {
	h := NewHarness(t, nil)
	user := remoteca.Principal(TestBench)
	methods := map[string]ssh.AuthMethod{
		"none":                 nil,
		"password":             ssh.Password("secret"),
		"keyboard-interactive": ssh.KeyboardInteractive(func(string, string, []string, []bool) ([]string, error) { return []string{"secret"}, nil }),
		"plain key":            ssh.PublicKeys(h.Key),
	}
	for name, m := range methods {
		var auth []ssh.AuthMethod
		if m != nil {
			auth = append(auth, m)
		}
		if _, _, err := h.DialPipe(t, user, auth...); err == nil {
			t.Errorf("%s: authenticated", name)
		}
	}
	if n := h.Lease.Count(); n != 0 {
		t.Fatalf("Admit called %d times", n)
	}
	if n := len(h.Launches.Started()); n != 0 {
		t.Fatalf("Start called %d times", n)
	}
}

func TestRefusedCertificates(t *testing.T) {
	h := NewHarness(t, nil)
	other, err := remoteca.Open(filepath.Join(t.TempDir(), "remote"))
	if err != nil {
		t.Fatal(err)
	}
	foreign := Issue(t, other, h.Key, WriterClaims(), time.Now())
	cases := []struct {
		name   string
		user   string
		signer ssh.Signer
		setup  func()
		admits bool // Admit is reached: the certificate itself verified
	}{
		{name: "wrong bench user", user: remoteca.Principal(strings.Repeat("a", 32)), signer: CertSigner(t, h.Key, h.Issued)},
		{name: "expired", user: remoteca.Principal(TestBench), signer: CertSigner(t, h.Key, h.Issued), setup: func() { h.Clock.Set(40 * time.Minute) }},
		{name: "other CA", user: remoteca.Principal(TestBench), signer: CertSigner(t, h.Key, foreign)},
		{name: "key not the certificate's", user: remoteca.Principal(TestBench), signer: foreignKeySigner(t, h.Issued)},
		{name: "old generation", user: remoteca.Principal(TestBench), signer: CertSigner(t, h.Key, h.Issued), setup: func() { h.Lease.Set(remotestate.ErrRevoked) }, admits: true},
		{name: "state unavailable", user: remoteca.Principal(TestBench), signer: CertSigner(t, h.Key, h.Issued), setup: func() { h.Lease.Set(remotestate.ErrUnavailable) }, admits: true},
	}
	for _, tc := range cases {
		h.Clock.Set(0)
		h.Lease.Set(nil)
		before := h.Lease.Count()
		if tc.setup != nil {
			tc.setup()
		}
		if _, _, err := h.DialPipe(t, tc.user, ssh.PublicKeys(tc.signer)); err == nil {
			t.Errorf("%s: authenticated", tc.name)
		}
		if got := h.Lease.Count() > before; got != tc.admits {
			t.Errorf("%s: Admit reached %v, want %v", tc.name, got, tc.admits)
		}
	}
	if n := len(h.Launches.Started()); n != 0 {
		t.Fatalf("Start called %d times", n)
	}
	if n := len(h.Reg.snapshot()); n != 0 {
		t.Fatalf("%d connections registered", n)
	}
}

// A certificate offered without proof of the key (a query) is answered from
// Verify alone: the lease is never read for a client that cannot sign.
func TestAdmitOnlyAfterProofOfKey(t *testing.T) {
	h := NewHarness(t, nil)
	stolen := foreignKeySigner(t, h.Issued) // the certificate, someone else's key
	if _, _, err := h.DialPipe(t, remoteca.Principal(TestBench), ssh.PublicKeys(stolen)); err == nil {
		t.Fatal("authenticated with another key")
	}
	if n := h.Lease.Count(); n != 0 {
		t.Fatalf("Admit called %d times before the key was proven", n)
	}
}

// Offering keys costs at most maxKeyOffers certificate checks per
// connection, even when every offer is a valid certificate queried without a
// signature (which MaxAuthTries does not count).
func TestKeyOffersBounded(t *testing.T) {
	h := NewHarness(t, nil)
	cfg := serverConfig(h.Cfg)
	cert, _, _, _, err := ssh.ParseAuthorizedKey([]byte(h.Issued.Line))
	if err != nil {
		t.Fatal(err)
	}
	meta := fakeMeta(remoteca.Principal(TestBench))
	for i := range maxKeyOffers + 3 {
		_, err := cfg.PublicKeyCallback(meta, cert)
		if (err == nil) != (i < maxKeyOffers) {
			t.Fatalf("offer %d: %v", i+1, err)
		}
	}
	if n := h.Lease.Count(); n != 0 {
		t.Fatalf("Admit called %d times", n)
	}
}

type fakeMeta string

func (m fakeMeta) User() string        { return string(m) }
func (fakeMeta) SessionID() []byte     { return nil }
func (fakeMeta) ClientVersion() []byte { return nil }
func (fakeMeta) ServerVersion() []byte { return nil }
func (fakeMeta) RemoteAddr() net.Addr  { return &net.TCPAddr{} }
func (fakeMeta) LocalAddr() net.Addr   { return &net.TCPAddr{} }

// A panic in one connection's handshake closes that connection only.
func TestPanicClosesOnlyItsConnection(t *testing.T) {
	h := NewHarness(t, nil)
	addr := h.ServeTCP(t)
	h.Lease.SetPanics(true)
	before := panics.Load()
	if _, err := ssh.Dial("tcp", addr, h.ClientConfig(remoteca.Principal(TestBench), h.CertAuth(t))); err == nil {
		t.Fatal("authenticated through a panicking Admit")
	}
	if panics.Load() != before+1 {
		t.Fatal("the panic was not recovered by its connection")
	}
	panics.Add(^uint64(0)) // the one expected panic
	h.Lease.SetPanics(false)
	c := h.DialTCP(t, addr, CertSigner(t, h.Key, h.Issued))
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

// The server's banner is exactly its version line: no build, host or lease.
func TestServerBannerIsOnlyTheVersion(t *testing.T) {
	h := NewHarness(t, nil)
	nc, _ := h.Pipe(t)
	line, err := bufio.NewReader(nc).ReadString('\n')
	if err != nil || line != ServerVersion+"\r\n" {
		t.Fatalf("banner %q %v", line, err)
	}
}

// Connections past MaxUnauthenticated in the handshake are closed before the
// server writes anything; finished connections give their slots back.
func TestUnauthenticatedLimit(t *testing.T) {
	h := NewHarness(t, nil)
	addr := h.ServeTCP(t)
	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for range MaxUnauthenticated {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
		if line, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.HasPrefix(line, "SSH-2.0-") {
			t.Fatalf("banner %q %v", line, err)
		}
	}
	extra, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = extra.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := extra.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("connection past the limit read %d bytes, %v", n, err)
	}
	_ = extra.Close()
	for _, c := range held {
		_ = c.Close()
	}
	held = nil
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.Reg.mu.Lock()
		total, unauth := h.Reg.total, h.Reg.unauth
		h.Reg.mu.Unlock()
		if total == 0 && unauth == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry holds %d connections, %d unauthenticated", total, unauth)
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.DialTCP(t, addr, CertSigner(t, h.Key, h.Issued))
}

func TestServeRefusesIncompleteConfig(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(t.Context(), l, Config{}); err != ErrConfig {
		t.Fatalf("Serve: %v", err)
	}
	if _, err := l.Accept(); err == nil {
		t.Fatal("listener left open")
	}
}

// A client that connects and sends nothing is closed after HandshakeTimeout.
func TestHandshakeTimeout(t *testing.T) {
	t.Parallel()
	h := NewHarness(t, nil)
	addr := h.ServeTCP(t)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	_ = c.SetReadDeadline(start.Add(HandshakeTimeout + 5*time.Second))
	_, err = io.Copy(io.Discard, c)
	if err != nil || time.Since(start) < HandshakeTimeout-time.Second {
		t.Fatalf("closed after %v: %v", time.Since(start), err)
	}
}
