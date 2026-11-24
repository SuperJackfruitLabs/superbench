package remotessh

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// clientFirstFlight is what a real client sends first: its version line and
// KEXINIT, as a seed the fuzzer can mutate past the version exchange.
func clientFirstFlight(t testing.TB) []byte {
	t.Helper()
	server, client := socketPair(t)
	defer server.Close()
	go func() {
		_, _, _, _ = ssh.NewClientConn(client, "seed", &ssh.ClientConfig{User: "b-0", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
		_ = client.Close()
	}()
	_, _ = server.Write([]byte(ServerVersion + "\r\n"))
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, _ := io.ReadAtLeast(server, buf, 64)
	return buf[:n]
}

func FuzzHandshake(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
	f.Add([]byte("SSH-2.0-x\r\n\x00\x00\x00\x0c\x0a\x14\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("garbage\r\nSSH-1.99-old\r\n"))
	f.Add(clientFirstFlight(f))
	h := NewHarness(f, nil)
	f.Fuzz(func(t *testing.T, raw []byte) {
		recovered := panics.Load()
		defer noPanics(t, recovered)
		server, client := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			HandleConn(context.Background(), server, h.Cfg)
		}()
		go func() { _, _ = io.Copy(io.Discard, client) }()
		_ = client.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = client.Write(raw)
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("HandleConn still running a second after the client left")
		}
		if n := h.Lease.Count(); n != 0 {
			t.Fatalf("Admit called %d times", n)
		}
		if n := len(h.Launches.Started()); n != 0 {
			t.Fatalf("Start called %d times", n)
		}
		h.Reg.mu.Lock()
		total, unauth := h.Reg.total, h.Reg.unauth
		h.Reg.mu.Unlock()
		if total != 0 || unauth != 0 {
			t.Fatalf("registry holds %d connections, %d unauthenticated", total, unauth)
		}
	})
}

var requestTypes = []string{"pty-req", "window-change", "shell", "exec", "subsystem", "env", "x11-req", "auth-agent-req@openssh.com", "signal", "break", "keepalive@openssh.com", "exit-status"}

type fuzzRequest struct {
	kind      string
	wantReply bool
	payload   []byte
}

// decodeRequests reads up to 16 requests: a selector byte (a known type, or
// a raw type name of up to 15 bytes), a reply flag in its top bit, a
// two-byte payload length and the payload.
func decodeRequests(raw []byte) []fuzzRequest {
	var out []fuzzRequest
	for len(raw) >= 3 && len(out) < 16 {
		sel := raw[0]
		r := fuzzRequest{wantReply: sel&0x80 == 0}
		sel &= 0x7f
		raw = raw[1:]
		if int(sel) < len(requestTypes) {
			r.kind = requestTypes[sel]
		} else {
			n := min(int(sel%16), len(raw))
			r.kind, raw = string(raw[:n]), raw[n:]
		}
		if len(raw) < 2 {
			break
		}
		n := min(int(binary.BigEndian.Uint16(raw)), len(raw)-2)
		r.payload, raw = raw[2:2+n], raw[2+n:]
		out = append(out, r)
	}
	return out
}

func encodeRequests(rs ...fuzzRequest) []byte {
	var out []byte
	for _, r := range rs {
		sel := -1
		for i, k := range requestTypes {
			if k == r.kind {
				sel = i
			}
		}
		b := byte(sel)
		if !r.wantReply {
			b |= 0x80
		}
		out = append(out, b)
		out = binary.BigEndian.AppendUint16(out, uint16(len(r.payload)))
		out = append(out, r.payload...)
	}
	return out
}

func validSpec(s SessionSpec) bool {
	switch s.Mode {
	case "shell":
		if s.Command != "" {
			return false
		}
	case "exec":
		if !validCommand(s.Command) {
			return false
		}
	case "sftp":
		if s.Command != "" || s.PTY {
			return false
		}
	default:
		return false
	}
	if s.PTY != (s.Term != "") || s.PTY && !termPattern.MatchString(s.Term) || s.Cols > maxPTYSize || s.Rows > maxPTYSize {
		return false
	}
	return true
}

func FuzzSessionRequests(f *testing.F) {
	pty := ssh.Marshal(ptyRequestMsg{Term: "xterm", Cols: 80, Rows: 24})
	f.Add(encodeRequests(fuzzRequest{"exec", true, ssh.Marshal(execMsg{"id"})}, fuzzRequest{"exec", true, ssh.Marshal(execMsg{"id"})}))
	f.Add(encodeRequests(fuzzRequest{"pty-req", true, pty}, fuzzRequest{"window-change", false, ssh.Marshal(windowChangeMsg{100, 30, 0, 0})}, fuzzRequest{"shell", true, nil}))
	f.Add(encodeRequests(fuzzRequest{"subsystem", true, ssh.Marshal(subsystemMsg{"sftp"})}, fuzzRequest{"shell", true, nil}))
	f.Add(encodeRequests(fuzzRequest{"env", true, ssh.Marshal(struct{ K, V string }{"A", "B"})}, fuzzRequest{"x11-req", true, nil}, fuzzRequest{"auth-agent-req@openssh.com", true, nil}, fuzzRequest{"signal", false, ssh.Marshal(struct{ S string }{"KILL"})}))
	f.Add(encodeRequests(fuzzRequest{"pty-req", true, ssh.Marshal(ptyRequestMsg{Term: "x", Cols: 1001, Rows: 1})}, fuzzRequest{"exec", true, []byte{0, 0, 0, 1, 0}}))
	h := NewHarness(f, nil)
	f.Fuzz(func(t *testing.T, raw []byte) {
		recovered := panics.Load()
		defer noPanics(t, recovered)
		c, done := h.Connect(t)
		ch, reqs, err := c.OpenChannel("session", nil)
		if err != nil {
			t.Fatal(err)
		}
		go ssh.DiscardRequests(reqs)
		before := len(h.Launches.Started())
		for _, r := range decodeRequests(raw) {
			if _, err := ch.SendRequest(r.kind, r.wantReply, r.payload); err != nil {
				break // the server closed the channel after a failed start
			}
		}
		// A request without a reply is handled before the channel's close.
		_ = ch.Close()
		_ = c.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("HandleConn still running after the client left")
		}
		started := h.Launches.Started()[before:]
		if len(started) > 1 {
			t.Fatalf("started %d times on one channel: %+v", len(started), started)
		}
		for _, s := range started {
			if !validSpec(s) {
				t.Fatalf("started an invalid session %+v", s)
			}
		}
	})
}

func noPanics(t *testing.T, before uint64) {
	if n := panics.Load() - before; n != 0 {
		t.Fatalf("%d panics recovered", n)
	}
}
