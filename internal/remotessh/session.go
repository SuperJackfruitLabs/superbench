package remotessh

import (
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
)

const (
	maxCommand = 64 << 10 // payloadwire.MaxSessionCommand
	maxPTYSize = 1000     // columns and rows
	// defaultTerm stands in for an empty TERM: OpenSSH sends "" when the
	// client has none, and the helper requires a name with every PTY.
	defaultTerm = "dumb"
	// ptyDrain is how long a PTY must stay quiet after its process exited
	// before the server stops reading it.
	ptyDrain = 100 * time.Millisecond
)

// termPattern is the helper's own TERM rule (payloadwire).
var termPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)

type ptyRequestMsg struct {
	Term                      string
	Cols, Rows, Width, Height uint32
	Modes                     string
}

type windowChangeMsg struct{ Cols, Rows, Width, Height uint32 }

type execMsg struct{ Command string }

type subsystemMsg struct{ Name string }

type exitStatusMsg struct{ Status uint32 }

type exitSignalMsg struct {
	Signal      string
	CoreDumped  bool
	Error, Lang string
}

// session is one session channel. The request loop alone touches started,
// failed, pty and master; mu guards what the connection's close and the
// process waiter share.
type session struct {
	x       *conn
	ch      ssh.Channel
	started bool // a shell, exec or subsystem request was taken
	failed  bool // the start failed: close the channel after the reply
	pty     *ptyRequestMsg
	master  *os.File
	done    chan struct{} // closed when the started process was reaped

	in, out atomic.Uint64

	mu      sync.Mutex
	audited bool
	auditID int64
	reason  string // the first end reason
	proc    Process
	exited  bool
	files   []*os.File // the server's ends of the session's descriptors
}

// serve answers the channel's requests until the client closes it or the
// connection ends, then ends the session and waits for its process.
func (s *session) serve(reqs <-chan *ssh.Request) {
	defer s.x.detach(s)
	for req := range reqs {
		ok, run := s.handle(req)
		if req.WantReply {
			_ = req.Reply(ok, nil)
		}
		if run != nil {
			go s.x.guard(run)
		}
		if s.failed {
			s.failed = false
			_ = s.ch.Close()
		}
	}
	s.end("closed")
	if s.done != nil {
		<-s.done
	}
	_ = s.ch.Close()
}

// handle answers one request. Only pty-req, window-change, shell, exec and the
// sftp subsystem can succeed; env, x11-req, auth-agent-req@openssh.com,
// signal and anything else is refused.
func (s *session) handle(req *ssh.Request) (bool, func()) {
	switch req.Type {
	case "pty-req":
		var m ptyRequestMsg
		if s.started || s.pty != nil || !ptySupported || !s.x.permitPTY || ssh.Unmarshal(req.Payload, &m) != nil {
			return false, nil
		}
		if m.Term == "" {
			m.Term = defaultTerm // a client with no TERM of its own, as "ssh -tt" from a script
		}
		if !termPattern.MatchString(m.Term) || m.Cols > maxPTYSize || m.Rows > maxPTYSize {
			return false, nil
		}
		s.pty = &m
		return true, nil
	case "window-change":
		var m windowChangeMsg
		if s.pty == nil || ssh.Unmarshal(req.Payload, &m) != nil || m.Cols > maxPTYSize || m.Rows > maxPTYSize {
			return false, nil
		}
		s.pty.Cols, s.pty.Rows = m.Cols, m.Rows
		if s.master != nil {
			_ = setWinsize(s.master, m.Cols, m.Rows)
		}
		return true, nil
	case "shell":
		if len(req.Payload) != 0 {
			return false, nil
		}
		return s.start(SessionSpec{Mode: "shell"})
	case "exec":
		var m execMsg
		if ssh.Unmarshal(req.Payload, &m) != nil || !validCommand(m.Command) {
			return false, nil
		}
		return s.start(SessionSpec{Mode: "exec", Command: m.Command})
	case "subsystem":
		var m subsystemMsg
		if ssh.Unmarshal(req.Payload, &m) != nil || m.Name != "sftp" {
			return false, nil
		}
		return s.start(SessionSpec{Mode: "sftp"})
	}
	return false, nil
}

func validCommand(c string) bool {
	return c != "" && len(c) <= maxCommand && utf8.ValidString(c) && !strings.ContainsRune(c, 0)
}

// start takes the channel's one start request: the lease is admitted again,
// the session is audited (which admits once more, in the audit's own
// transaction), and the launcher starts it. Any failure refuses the request
// and closes the channel; the channel never starts twice.
func (s *session) start(spec SessionSpec) (bool, func()) {
	if s.started {
		return false, nil
	}
	s.started = true
	s.failed = true
	if s.pty != nil && spec.Mode != "sftp" {
		spec.PTY, spec.Term, spec.Cols, spec.Rows = true, s.pty.Term, s.pty.Cols, s.pty.Rows
	}
	c := s.x.cfg
	a, err := c.admit(s.x.claims)
	if err != nil {
		return false, nil
	}
	id, err := c.Audit.SessionStarted(a, remoteschema.SessionRecord{Serial: s.x.serial, Key: a.Key, BenchID: a.BenchID, Fingerprint: s.x.fingerprint, Source: s.x.source, Kind: spec.Mode, CommandName: remoteschema.CommandName(spec.Command)})
	if err != nil {
		return false, nil
	}
	s.mu.Lock()
	s.audited, s.auditID = true, id
	s.mu.Unlock()
	if reason, ok := s.x.attach(s); !ok {
		s.end(reason)
		return false, nil
	}
	ours, theirs, err := s.plumb(spec)
	if err != nil {
		s.end("refused")
		return false, nil
	}
	proc, err := c.Launcher.Start(s.x.ctx, a, spec, theirs)
	if err != nil || proc == nil {
		closeFiles(ours)
		s.end("refused")
		return false, nil
	}
	s.mu.Lock()
	s.proc, s.files = proc, ours
	reason := s.reason
	s.mu.Unlock()
	s.done = make(chan struct{})
	if reason != "" {
		// Ended while starting: stop it, but still reap it and count it.
		proc.Terminate()
		closeFiles(ours)
	}
	s.failed = reason != ""
	return reason == "", func() { s.run(proc, spec.PTY, ours) }
}

// plumb makes the session's descriptors: a PTY (the master stays here, the
// slave goes to the launcher), or three pipes whose stdin read end and
// stdout and stderr write ends go to the launcher.
func (s *session) plumb(spec SessionSpec) (ours, theirs []*os.File, err error) {
	if spec.PTY {
		master, slave, hold, err := openPTY()
		if err != nil {
			return nil, nil, err
		}
		if spec.Cols > 0 && spec.Rows > 0 {
			_ = setWinsize(master, spec.Cols, spec.Rows)
		}
		s.master = master
		return []*os.File{master, hold}, []*os.File{slave}, nil
	}
	var files []*os.File
	for range 3 {
		r, w, err := os.Pipe()
		if err != nil {
			closeFiles(files)
			return nil, nil, err
		}
		files = append(files, r, w)
	}
	// files: in(r,w), out(r,w), err(r,w)
	return []*os.File{files[1], files[2], files[4]}, []*os.File{files[0], files[3], files[5]}, nil
}

// run copies the session's bytes, counting them, until the process exits and
// its output is drained; then it sends exit-status or exit-signal, records
// the end and closes the channel.
func (s *session) run(proc Process, pty bool, ours []*os.File) {
	defer close(s.done)
	var outputs sync.WaitGroup
	exited := make(chan struct{})
	in := &countingReader{r: s.ch, n: &s.in}
	if pty {
		master := ours[0]
		go s.x.guard(func() { _, _ = io.Copy(master, in) })
		outputs.Add(1)
		go s.x.guard(func() {
			defer outputs.Done()
			drainPTY(&countingWriter{w: s.ch, n: &s.out}, master, exited)
		})
	} else {
		stdin, stdout, stderr := ours[0], ours[1], ours[2]
		go s.x.guard(func() {
			_, _ = io.Copy(stdin, in)
			_ = stdin.Close()
		})
		outputs.Add(2)
		go s.x.guard(func() {
			defer outputs.Done()
			_, _ = io.Copy(&countingWriter{w: s.ch, n: &s.out}, stdout)
		})
		go s.x.guard(func() {
			defer outputs.Done()
			_, _ = io.Copy(&countingWriter{w: s.ch.Stderr(), n: &s.out}, stderr)
		})
	}
	code, signal, err := proc.Wait()
	s.mu.Lock()
	s.exited = true
	s.mu.Unlock()
	close(exited)
	if pty {
		// The server's own slave keeps the master from EIO: wake the reader
		// so it drains what the session wrote, then stops when quiet.
		_ = ours[0].SetReadDeadline(time.Now().Add(ptyDrain))
	}
	outputs.Wait()
	closeFiles(ours)
	if err == nil {
		_ = s.ch.CloseWrite()
		if signal != "" {
			_, _ = s.ch.SendRequest("exit-signal", false, ssh.Marshal(exitSignalMsg{Signal: signal}))
		} else if code >= 0 {
			_, _ = s.ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusMsg{Status: uint32(code)}))
		}
	}
	s.end("exit")
	s.mu.Lock()
	id, reason := s.auditID, s.reason
	s.mu.Unlock()
	// The final counts; the first end reason stays.
	_ = s.x.cfg.Audit.SessionEnded(id, s.in.Load(), s.out.Load(), reason)
	_ = s.ch.Close()
}

// drainPTY copies the master to w. Once the process exited, each read
// extends the deadline by ptyDrain, so output is drained until the terminal
// stays quiet that long; a slow client never shortens it.
func drainPTY(w io.Writer, master *os.File, exited <-chan struct{}) {
	buf := make([]byte, 32<<10)
	for {
		n, err := master.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
		select {
		case <-exited:
			_ = master.SetReadDeadline(time.Now().Add(ptyDrain))
		default:
		}
	}
}

// end ends the session once, with the first reason: the audit row is ended
// now, and a running process is terminated and its descriptors closed.
func (s *session) end(reason string) {
	s.mu.Lock()
	if s.reason != "" {
		s.mu.Unlock()
		return
	}
	s.reason = reason
	audited, id, proc, exited, files := s.audited, s.auditID, s.proc, s.exited, s.files
	s.mu.Unlock()
	if audited {
		_ = s.x.cfg.Audit.SessionEnded(id, s.in.Load(), s.out.Load(), reason)
	}
	if reason == "exit" {
		return
	}
	if proc != nil && !exited {
		proc.Terminate()
	}
	closeFiles(files)
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

type countingReader struct {
	r io.Reader
	n *atomic.Uint64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(uint64(max(n, 0)))
	return n, err
}

type countingWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(uint64(max(n, 0)))
	return n, err
}
