package remotessh

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

func rejection(err error) ssh.RejectionReason {
	var open *ssh.OpenChannelError
	if errors.As(err, &open) {
		return open.Reason
	}
	return 0
}

func TestChannelRecheck(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	h.Lease.Set(remotestate.ErrRevoked)
	if _, _, err := c.OpenChannel("session", nil); rejection(err) != ssh.Prohibited {
		t.Fatalf("session channel after revocation: %v", err)
	}
	if n := len(h.Launches.Started()); n != 0 {
		t.Fatalf("Start called %d times", n)
	}
}

// A lease revoked between the channel open and its start request refuses the
// start: the lease is admitted again, and the audit admits it once more.
func TestStartRecheck(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	h.Lease.Set(remotestate.ErrRevoked)
	if ok, _ := ch.SendRequest("shell", true, nil); ok {
		t.Fatal("shell started after revocation")
	}
	if n := len(h.Launches.Started()); n != 0 || len(h.Log.Snapshot()) != 0 {
		t.Fatalf("Start called %d times, audit %+v", n, h.Log.Snapshot())
	}
}

func TestForwardingAndExtrasRefused(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	if _, err := c.Dial("tcp", "127.0.0.1:9"); err == nil {
		t.Fatal("direct-tcpip opened")
	}
	for _, kind := range []string{"direct-tcpip", "forwarded-tcpip", "x11", "auth-agent@openssh.com", "direct-streamlocal@openssh.com", "tun@openssh.com", "bogus"} {
		if _, _, err := c.OpenChannel(kind, nil); rejection(err) != ssh.Prohibited {
			t.Errorf("channel %q: %v", kind, err)
		}
	}
	if _, err := c.Listen("tcp", "127.0.0.1:0"); err == nil {
		t.Fatal("tcpip-forward accepted")
	}
	for _, global := range []string{"tcpip-forward", "streamlocal-forward@openssh.com", "cancel-tcpip-forward", "no-more-sessions@openssh.com", "keepalive@openssh.com"} {
		if ok, _, err := c.SendRequest(global, true, ssh.Marshal(struct {
			Addr string
			Port uint32
		}{"127.0.0.1", 0})); ok || err != nil {
			t.Errorf("global %q: %v %v", global, ok, err)
		}
	}
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	refused := map[string][]byte{
		"auth-agent-req@openssh.com": nil,
		"x11-req": ssh.Marshal(struct {
			Single        bool
			Proto, Cookie string
			Screen        uint32
		}{false, "MIT-MAGIC-COOKIE-1", "00", 0}),
		"env":             ssh.Marshal(struct{ Name, Value string }{"LD_PRELOAD", "/tmp/x.so"}),
		"subsystem":       ssh.Marshal(subsystemMsg{"foo"}),
		"signal":          ssh.Marshal(struct{ Name string }{"KILL"}),
		"window-change":   ssh.Marshal(windowChangeMsg{80, 24, 0, 0}), // no PTY
		"xon-xoff":        ssh.Marshal(struct{ B bool }{true}),
		"break":           ssh.Marshal(struct{ Ms uint32 }{10}),
		"eow@openssh.com": nil,
		"bogus":           nil,
	}
	for kind, payload := range refused {
		if ok, err := ch.SendRequest(kind, true, payload); ok || err != nil {
			t.Errorf("request %q: %v %v", kind, ok, err)
		}
	}
	if n := len(h.Launches.Started()); n != 0 {
		t.Fatalf("Start called %d times", n)
	}
	// The same channel still starts one valid session.
	if ok, _ := ch.SendRequest("subsystem", true, ssh.Marshal(subsystemMsg{"sftp"})); !ok {
		t.Fatal("sftp refused")
	}
	if s := h.Launches.Started(); len(s) != 1 || s[0] != (SessionSpec{Mode: "sftp"}) {
		t.Fatalf("started %+v", s)
	}
}

func TestOneStartPerChannel(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if ok, _ := ch.SendRequest("exec", true, ssh.Marshal(execMsg{"sleep 30"})); !ok {
		t.Fatal("first exec refused")
	}
	for _, r := range []struct {
		kind    string
		payload []byte
	}{{"exec", ssh.Marshal(execMsg{"id"})}, {"shell", nil}, {"subsystem", ssh.Marshal(subsystemMsg{"sftp"})}, {"pty-req", ssh.Marshal(ptyRequestMsg{Term: "xterm", Cols: 80, Rows: 24})}} {
		if ok, _ := ch.SendRequest(r.kind, true, r.payload); ok {
			t.Errorf("%s accepted after the start", r.kind)
		}
	}
	if s := h.Launches.Started(); len(s) != 1 || s[0] != (SessionSpec{Mode: "exec", Command: "sleep 30"}) {
		t.Fatalf("started %+v", s)
	}
	// A failed first start still counts: the channel closes.
	h2 := NewHarness(t, nil)
	c2, _ := h2.Connect(t)
	ch2, reqs2, err := c2.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs2)
	h2.Lease.Set(remotestate.ErrRevoked)
	if ok, _ := ch2.SendRequest("shell", true, nil); ok {
		t.Fatal("shell started while revoked")
	}
	h2.Lease.Set(nil)
	if ok, _ := ch2.SendRequest("shell", true, nil); ok {
		t.Fatal("second start accepted")
	}
	if n := len(h2.Launches.Started()); n != 0 {
		t.Fatalf("Start called %d times", n)
	}
}

func TestStartRequestLimits(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	bad := []struct {
		name, kind string
		payload    []byte
	}{
		{"exec empty", "exec", ssh.Marshal(execMsg{""})},
		{"exec over 64 KiB", "exec", ssh.Marshal(execMsg{strings.Repeat("a", maxCommand+1)})},
		{"exec invalid UTF-8", "exec", ssh.Marshal(execMsg{"echo \xff"})},
		{"exec NUL", "exec", ssh.Marshal(execMsg{"echo a\x00b"})},
		{"exec trailing bytes", "exec", append(ssh.Marshal(execMsg{"id"}), 0)},
		{"exec truncated", "exec", []byte{0, 0, 0, 9, 'i'}},
		{"shell with payload", "shell", []byte{0}},
		{"subsystem SFTP", "subsystem", ssh.Marshal(subsystemMsg{"SFTP"})},
		{"pty columns", "pty-req", ssh.Marshal(ptyRequestMsg{Term: "xterm", Cols: maxPTYSize + 1, Rows: 24})},
		{"pty rows", "pty-req", ssh.Marshal(ptyRequestMsg{Term: "xterm", Cols: 80, Rows: 1 << 31})},
		{"pty term", "pty-req", ssh.Marshal(ptyRequestMsg{Term: "xterm;rm", Cols: 80, Rows: 24})},
		{"pty long term", "pty-req", ssh.Marshal(ptyRequestMsg{Term: strings.Repeat("x", 65), Cols: 80, Rows: 24})},
	}
	for _, b := range bad {
		ch, reqs, err := c.OpenChannel("session", nil)
		if err != nil {
			t.Fatal(err)
		}
		go ssh.DiscardRequests(reqs)
		if ok, _ := ch.SendRequest(b.kind, true, b.payload); ok {
			t.Errorf("%s accepted", b.name)
		}
		_ = ch.Close()
	}
	if n := len(h.Launches.Started()); n != 0 {
		t.Fatalf("Start called %d times", n)
	}
	// The largest command is accepted.
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if ok, _ := ch.SendRequest("exec", true, ssh.Marshal(execMsg{strings.Repeat("a", maxCommand)})); !ok {
		t.Fatal("64 KiB command refused")
	}
}

func TestPTYRequest(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	ok, _ := ch.SendRequest("pty-req", true, ssh.Marshal(ptyRequestMsg{Term: "xterm-256color", Cols: maxPTYSize, Rows: 40}))
	if ok != ptySupported {
		t.Fatalf("pty-req %v on a platform with PTY support %v", ok, ptySupported)
	}
	if ok, _ := ch.SendRequest("pty-req", true, ssh.Marshal(ptyRequestMsg{Term: "xterm", Cols: 80, Rows: 24})); ok {
		t.Fatal("second pty-req accepted")
	}
	if !ptySupported {
		return
	}
	if ok, _ := ch.SendRequest("window-change", true, ssh.Marshal(windowChangeMsg{Cols: 120, Rows: 50})); !ok {
		t.Fatal("window-change refused")
	}
	if ok, _ := ch.SendRequest("window-change", true, ssh.Marshal(windowChangeMsg{Cols: 1001, Rows: 50})); ok {
		t.Fatal("oversized window-change accepted")
	}
	if ok, _ := ch.SendRequest("exec", true, ssh.Marshal(execMsg{"tty"})); !ok {
		t.Fatal("exec refused")
	}
	if s := h.Launches.Started(); len(s) != 1 || s[0] != (SessionSpec{Mode: "exec", Command: "tty", Term: "xterm-256color", PTY: true, Cols: 120, Rows: 50}) {
		t.Fatalf("started %+v", s)
	}
	// SFTP never gets the PTY.
	ch2, reqs2, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs2)
	_, _ = ch2.SendRequest("pty-req", true, ssh.Marshal(ptyRequestMsg{Term: "xterm", Cols: 80, Rows: 24}))
	if ok, _ := ch2.SendRequest("subsystem", true, ssh.Marshal(subsystemMsg{"sftp"})); !ok {
		t.Fatal("sftp refused")
	}
	if s := h.Launches.Started(); len(s) != 2 || s[1] != (SessionSpec{Mode: "sftp"}) {
		t.Fatalf("started %+v", s)
	}
	// A client with no TERM gets a dumb terminal.
	ch3, reqs3, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs3)
	if ok, _ := ch3.SendRequest("pty-req", true, ssh.Marshal(ptyRequestMsg{Cols: 0, Rows: 0})); !ok {
		t.Fatal("pty-req without a TERM refused")
	}
	if ok, _ := ch3.SendRequest("shell", true, nil); !ok {
		t.Fatal("shell refused")
	}
	if s := h.Launches.Started(); len(s) != 3 || s[2] != (SessionSpec{Mode: "shell", Term: "dumb", PTY: true}) {
		t.Fatalf("started %+v", s)
	}
}

func TestSessionLimitPerBench(t *testing.T) {
	h := NewHarness(t, nil)
	first, _ := h.Connect(t)
	second, _ := h.Connect(t)
	var open []ssh.Channel
	for i := range MaxSessionsPerBench {
		c := first
		if i%2 == 1 {
			c = second
		}
		ch, reqs, err := c.OpenChannel("session", nil)
		if err != nil {
			t.Fatalf("session %d: %v", i+1, err)
		}
		go ssh.DiscardRequests(reqs)
		open = append(open, ch)
	}
	for _, c := range []*ssh.Client{first, second} {
		if _, _, err := c.OpenChannel("session", nil); rejection(err) != ssh.ResourceShortage {
			t.Fatalf("session past the limit: %v", err)
		}
	}
	_ = open[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ch, reqs, err := first.OpenChannel("session", nil)
		if err == nil {
			go ssh.DiscardRequests(reqs)
			_ = ch.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot never returned: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Closing the channel while the command runs terminates it: "closed".
func TestClientCloseEndsSession(t *testing.T) {
	h := NewHarness(t, nil)
	c, _ := h.Connect(t)
	ch, reqs, err := c.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	if ok, _ := ch.SendRequest("exec", true, ssh.Marshal(execMsg{"sleep 60"})); !ok {
		t.Fatal("exec refused")
	}
	_ = ch.Close()
	h.Log.WaitReasons(t, 1, "closed", 5*time.Second)
	p := h.Launches.Procs()[0]
	select {
	case <-p.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("process not terminated")
	}
}
