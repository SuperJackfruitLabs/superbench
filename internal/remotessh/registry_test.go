package remotessh

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

// nearDeadline sets the clock so the harness certificate has left seconds.
func nearDeadline(h *Harness, left time.Duration) {
	h.Clock.Set(time.Until(h.Issued.ValidBefore) - left)
}

func startSleep(t *testing.T, c *ssh.Client) {
	t.Helper()
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if ok, _ := ch.SendRequest("exec", true, ssh.Marshal(execMsg{"sleep 60"})); !ok {
		t.Fatal("exec refused")
	}
}

func TestDeadlineClosesConnection(t *testing.T) {
	h := NewHarness(t, nil)
	nearDeadline(h, 2*time.Second)
	c, _ := h.Connect(t)
	startSleep(t, c)
	if !ClosedWithin(c, 3*time.Second) {
		t.Fatal("connection open past its certificate's ValidBefore")
	}
	h.Log.WaitReasons(t, 1, "expired", time.Second)
	select {
	case <-h.Launches.Procs()[0].Exited():
	case <-time.After(time.Second):
		t.Fatal("process not terminated")
	}
	if n := len(h.Reg.snapshot()); n != 0 {
		t.Fatalf("%d connections still registered", n)
	}
}

func TestExtendKeepsConnection(t *testing.T) {
	h := NewHarness(t, nil)
	other := NewKey(t)
	otherIssued := Issue(t, h.Auth, other, WriterClaims(), time.Now())
	nearDeadline(h, 2*time.Second)
	c, _ := h.Connect(t)
	o, _, err := h.DialPipe(t, remoteca.Principal(TestBench), ssh.PublicKeys(CertSigner(t, other, otherIssued)))
	if err != nil {
		t.Fatal(err)
	}
	// The renewal names this key and these claims: the other key's
	// connection, and claims of another generation, are not extended.
	h.Reg.Extend(h.Issued.Fingerprint, remoteca.Claims{BenchID: TestBench, LeaseID: TestLease, Generation: 4, ExecutionID: TestExec}, h.Issued.ValidBefore.Add(time.Hour))
	h.Reg.Extend("", WriterClaims(), h.Issued.ValidBefore.Add(time.Hour))
	h.Reg.Extend(h.Issued.Fingerprint, WriterClaims(), h.Issued.ValidBefore.Add(time.Minute))
	if !ClosedWithin(o, 3*time.Second) {
		t.Fatal("the other key's connection was extended")
	}
	time.Sleep(1500 * time.Millisecond) // past the original deadline
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatalf("connection closed despite renewal: %v", err)
	}
	go ssh.DiscardRequests(reqs)
	_ = ch.Close()
}

// A renewal never extends past one certificate's validity from now.
func TestExtendIsBounded(t *testing.T) {
	h := NewHarness(t, nil)
	h.Connect(t)
	h.Reg.Extend(h.Issued.Fingerprint, WriterClaims(), time.Now().Add(24*time.Hour))
	x := h.Reg.snapshot()[0]
	x.mu.Lock()
	deadline := x.deadline
	x.mu.Unlock()
	if limit := time.Now().Add(remoteca.WriterValidity + remoteca.ClockSkew + time.Second); deadline.After(limit) {
		t.Fatalf("deadline %v past %v", deadline, limit)
	}
}

// Review Focus 1 at this layer: a revoked lease closes every connection
// within the sweep, including ones with no channel open.
func TestSweepClosesIdleConnections(t *testing.T) {
	h := NewHarness(t, nil)
	addr := h.ServeTCP(t)
	a := h.DialTCP(t, addr, CertSigner(t, h.Key, h.Issued))
	b := h.DialTCP(t, addr, CertSigner(t, h.Key, h.Issued))
	time.Sleep(SweepInterval + 200*time.Millisecond)
	if ClosedWithin(a, 0) {
		t.Fatal("the sweep closed an admitted connection")
	}
	h.Lease.Set(remotestate.ErrRevoked)
	for _, c := range []*ssh.Client{a, b} {
		if !ClosedWithin(c, 2*time.Second) {
			t.Fatal("idle connection open after revocation")
		}
	}
}

// The sweep fails closed: a state error closes the connection too.
func TestSweepClosesOnUnavailableState(t *testing.T) {
	h := NewHarness(t, nil)
	addr := h.ServeTCP(t)
	c := h.DialTCP(t, addr, CertSigner(t, h.Key, h.Issued))
	startSleep(t, c)
	h.Lease.Set(remotestate.ErrUnavailable)
	if !ClosedWithin(c, 2*time.Second) {
		t.Fatal("connection open after the state became unavailable")
	}
	h.Log.WaitReasons(t, 1, "revoked", time.Second)
}

func TestServeShutdownClosesSessions(t *testing.T) {
	h := NewHarness(t, nil)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, h.Cfg) }()
	c := h.DialTCP(t, l.Addr().String(), CertSigner(t, h.Key, h.Issued))
	startSleep(t, c)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	// Serve returned only after the session ended.
	if rows := h.Log.Snapshot(); len(rows) != 1 || rows[0].EndReason != "closed" {
		t.Fatalf("audit %+v", rows)
	}
	if !ClosedWithin(c, time.Second) {
		t.Fatal("connection open after Serve returned")
	}
}
