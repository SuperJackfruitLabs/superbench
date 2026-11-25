package remotesession

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func admission() remotestate.Admission {
	return remotestate.Admission{Key: "example", BenchID: strings.Repeat("a", 32), LayoutID: strings.Repeat("a", 32), Root: "/data/benches/example", Execution: execution.Execution{ID: strings.Repeat("c", 32), Root: "/sys/fs/cgroup/superbench", BootID: "01234567-89ab-cdef-0123-456789abcdef", HostID: strings.Repeat("d", 64), Phase: "running", Kind: execution.KindRemote}}
}

func pipeFiles(t *testing.T) []*os.File {
	t.Helper()
	var files []*os.File
	for i := 0; i < 2; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, r, w)
	}
	t.Cleanup(func() { closeAll(files) })
	return files
}

func TestInstalledStartRefusesWithoutBinding(t *testing.T) {
	// No test host holds the installed binding for its own uid: the launcher
	// refuses before any socket, pipe or process exists, and closes fds.
	files := pipeFiles(t)
	process, err := Installed{}.Start(context.Background(), admission(), remotessh.SessionSpec{Mode: "shell"}, []*os.File{files[0], files[1], files[3]})
	if !errors.Is(err, serviceidentity.ErrAuthority) || process != nil {
		t.Fatalf("started without the installed binding: %v %v", process, err)
	}
	for _, f := range []*os.File{files[0], files[1], files[3]} {
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Start kept a session descriptor open: %v", err)
		}
	}
}

func TestSessionRequestFromAdmission(t *testing.T) {
	a := admission()
	r, err := sessionRequest(a, remotessh.SessionSpec{Mode: "exec", Command: "pwd", Term: "xterm", PTY: true, Cols: 80, Rows: 24}, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	want := payloadwire.SessionRequest{Version: payloadwire.SessionVersion, Key: "example", LayoutID: a.LayoutID, PolicyDigest: strings.Repeat("b", 64), Scope: payloadwire.Scope{ID: a.Execution.ID, HostID: a.Execution.HostID, BootID: a.Execution.BootID}, Mode: "exec", Command: "pwd", Term: "xterm"}
	if r != want {
		t.Fatalf("frame %#v", r)
	}
	for name, c := range map[string]struct {
		edit func(*remotestate.Admission)
		spec remotessh.SessionSpec
	}{
		"pty without term":   {func(*remotestate.Admission) {}, remotessh.SessionSpec{Mode: "shell", PTY: true}},
		"term without pty":   {func(*remotestate.Admission) {}, remotessh.SessionSpec{Mode: "shell", Term: "xterm"}},
		"sftp with pty":      {func(*remotestate.Admission) {}, remotessh.SessionSpec{Mode: "sftp", Term: "xterm", PTY: true}},
		"unknown mode":       {func(*remotestate.Admission) {}, remotessh.SessionSpec{Mode: "x11"}},
		"supervised run":     {func(a *remotestate.Admission) { a.Execution.Kind = "" }, remotessh.SessionSpec{Mode: "shell"}},
		"stopped execution":  {func(a *remotestate.Admission) { a.Execution.Phase = "stopped" }, remotessh.SessionSpec{Mode: "shell"}},
		"missing execution":  {func(a *remotestate.Admission) { a.Execution.ID = "" }, remotessh.SessionSpec{Mode: "shell"}},
		"invalid key":        {func(a *remotestate.Admission) { a.Key = "../other" }, remotessh.SessionSpec{Mode: "shell"}},
		"exec without input": {func(*remotestate.Admission) {}, remotessh.SessionSpec{Mode: "exec"}},
	} {
		a := admission()
		c.edit(&a)
		if _, err := sessionRequest(a, c.spec, strings.Repeat("b", 64)); !errors.Is(err, ErrSpec) {
			t.Errorf("%s framed: %v", name, err)
		}
	}
}

func TestRefusalKeepsOnlyTheStaticCode(t *testing.T) {
	for stderr, want := range map[string]string{
		"managed payload refused: descriptors\n":             "remote session refused: descriptors",
		"sudo: a password is required\n":                     "remote session refused: unknown",
		"managed payload refused: /etc/shadow\n":             "remote session refused: unknown",
		"managed payload refused: descriptors\nextra text\n": "remote session refused: unknown",
		"": "remote session refused: unknown",
	} {
		err := refusal([]byte(stderr))
		if !errors.Is(err, ErrRefused) || err.Error() != want {
			t.Errorf("%q -> %v", stderr, err)
		}
	}
}

// helperPeer plays the helper's side of the control socket.
func helperPeer(t *testing.T, peer int, reply string) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer syscall.Close(peer)
		var header [4]byte
		if _, err := io.ReadFull(fdReader(peer), header[:]); err != nil {
			done <- err
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(header[:]))
		if _, err := io.ReadFull(fdReader(peer), body); err != nil {
			done <- err
			return
		}
		if _, err := payloadwire.ParseSessionRequest(body); err != nil {
			done <- err
			return
		}
		data, oob := make([]byte, 2), make([]byte, syscall.CmsgSpace(4*4))
		n, oobn, _, _, err := syscall.Recvmsg(peer, data, oob, 0)
		if err != nil || n != 1 || data[0] != 'F' {
			done <- errors.Join(errors.New("descriptor message"), err)
			return
		}
		messages, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err != nil || len(messages) != 1 {
			done <- errors.Join(errors.New("control message"), err)
			return
		}
		fds, err := syscall.ParseUnixRights(&messages[0])
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
		if err != nil || len(fds) != 3 {
			done <- errors.New("descriptor count")
			return
		}
		if n, err := syscall.Read(peer, data); n != 0 || err != nil {
			done <- errors.New("no end of stream after the descriptors")
			return
		}
		if reply != "" {
			_, _ = syscall.Write(peer, []byte(reply))
		}
		done <- nil
	}()
	return done
}

type fdReader int

func (f fdReader) Read(p []byte) (int, error) {
	n, err := syscall.Read(int(f), p)
	if n == 0 && err == nil {
		return 0, io.EOF
	}
	return n, err
}

func TestHandshakeNeedsExecReached(t *testing.T) {
	r, err := sessionRequest(admission(), remotessh.SessionSpec{Mode: "shell"}, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	body, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	for reply, ok := range map[string]bool{payloadwire.ExecReached: true, "": false, payloadwire.ExecFailed: false, payloadwire.ExecReached[:5]: false} {
		pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		peer := helperPeer(t, pair[1], reply)
		err = handshake(context.Background(), pair[0], body, pipeFiles(t)[:3], time.Now().Add(5*time.Second))
		_ = syscall.Close(pair[0])
		if peerErr := <-peer; peerErr != nil {
			t.Fatalf("helper side: %v", peerErr)
		}
		if ok != (err == nil) || !ok && !errors.Is(err, ErrRefused) {
			t.Errorf("reply %q: %v", reply, err)
		}
	}
	// A helper that never answers is cut off by the context.
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(pair[0])
	defer syscall.Close(pair[1])
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := handshake(ctx, pair[0], body, pipeFiles(t)[:3], time.Now().Add(StartTimeout)); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("silent helper: %v after %v", err, time.Since(start))
	}
}
