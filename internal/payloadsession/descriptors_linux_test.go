//go:build linux

package payloadsession

import (
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func socketPair(t *testing.T, kind int) (int, int) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, kind|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fds[0], fds[1]
}

func pipes(t *testing.T, n int) []*os.File {
	t.Helper()
	var files []*os.File
	for i := 0; i < n; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, r, w)
	}
	t.Cleanup(func() {
		for _, f := range files {
			_ = f.Close()
		}
	})
	return files
}

func rightsOf(files ...*os.File) []byte {
	var fds []int
	for _, f := range files {
		fds = append(fds, int(f.Fd()))
	}
	return syscall.UnixRights(fds...)
}

// send writes data with descriptors attached, then optional trailing bytes and
// optionally the end-of-stream the daemon gives with shutdown(SHUT_WR).
func send(t *testing.T, sock int, data []byte, oob []byte, trailing []byte, eof bool) {
	t.Helper()
	if err := syscall.Sendmsg(sock, data, oob, nil, 0); err != nil {
		t.Fatal(err)
	}
	if len(trailing) != 0 {
		if _, err := syscall.Write(sock, trailing); err != nil {
			t.Fatal(err)
		}
	}
	if eof {
		if err := syscall.Shutdown(sock, syscall.SHUT_WR); err != nil {
			t.Fatal(err)
		}
	}
}

func closeAll(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

func TestReceiveExactDescriptorCount(t *testing.T) {
	p := pipes(t, 2) // r0 w0 r1 w1
	for _, c := range []struct {
		sent []*os.File
		ok   bool
	}{{[]*os.File{p[0], p[1], p[3]}, true}, {[]*os.File{p[0], p[1]}, false}, {[]*os.File{p[0], p[1], p[3], p[2]}, false}} {
		parent, child := socketPair(t, syscall.SOCK_STREAM)
		before := openFDs(t)
		send(t, parent, []byte{'F'}, rightsOf(c.sent...), nil, true)
		got, err := Receive(child, 3, time.Second)
		if c.ok != (err == nil) || err != nil && !errors.Is(err, ErrSession) {
			t.Fatalf("%d sent: %v", len(c.sent), err)
		}
		if c.ok {
			if len(got) != 3 {
				t.Fatalf("received %d", len(got))
			}
			for i, f := range got {
				flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFD, 0)
				if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
					t.Fatalf("received descriptor %d is inheritable", i)
				}
			}
			closeAll(got)
		} else if got != nil {
			t.Fatal("refusal returned descriptors")
		}
		if after := openFDs(t); after != before {
			t.Fatalf("%d sent: descriptors leaked: %d -> %d", len(c.sent), before, after)
		}
		_ = syscall.Close(parent)
		_ = syscall.Close(child)
	}
}

func TestReceiveRefusesTruncationAndExtraData(t *testing.T) {
	p := pipes(t, 4)
	cases := map[string]func(parent int){
		"truncated":     func(s int) { send(t, s, []byte{'F'}, rightsOf(p...), nil, true) },
		"wrong byte":    func(s int) { send(t, s, []byte{'G'}, rightsOf(p[0], p[1], p[3]), nil, true) },
		"trailing data": func(s int) { send(t, s, []byte{'F'}, rightsOf(p[0], p[1], p[3]), []byte{'x'}, true) },
		"two bytes":     func(s int) { send(t, s, []byte{'F', 'F'}, rightsOf(p[0], p[1], p[3]), nil, true) },
		"second message": func(s int) {
			send(t, s, []byte{'F'}, rightsOf(p[0], p[1], p[3]), nil, false)
			send(t, s, []byte{'F'}, rightsOf(p[2], p[5], p[7]), nil, true)
		},
		"no descriptors": func(s int) { send(t, s, []byte{'F'}, nil, nil, true) },
		"empty":          func(s int) { send(t, s, nil, nil, nil, true) },
		"no end":         func(s int) { send(t, s, []byte{'F'}, rightsOf(p[0], p[1], p[3]), nil, false) },
		"silent":         func(int) {},
	}
	for name, setup := range cases {
		parent, child := socketPair(t, syscall.SOCK_STREAM)
		before := openFDs(t)
		setup(parent)
		start := time.Now()
		got, err := Receive(child, 3, 200*time.Millisecond)
		if !errors.Is(err, ErrSession) || got != nil {
			closeAll(got)
			t.Errorf("%s: accepted (%v)", name, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s: no deadline", name)
		}
		if after := openFDs(t); after != before {
			t.Errorf("%s: descriptors leaked: %d -> %d", name, before, after)
		}
		_ = syscall.Close(parent)
		_ = syscall.Close(child)
	}
	parent, child := socketPair(t, syscall.SOCK_DGRAM)
	defer syscall.Close(parent)
	defer syscall.Close(child)
	send(t, parent, []byte{'F'}, rightsOf(p[0], p[1], p[3]), nil, false)
	if got, err := Receive(child, 3, 200*time.Millisecond); !errors.Is(err, ErrSession) {
		closeAll(got)
		t.Error("datagram control socket accepted")
	}
	for _, want := range []int{0, 2, 4} {
		if _, err := Receive(child, want, time.Second); !errors.Is(err, ErrSession) {
			t.Errorf("want %d accepted", want)
		}
	}
	if _, err := Receive(child, 3, time.Minute); !errors.Is(err, ErrSession) {
		t.Error("unbounded timeout accepted")
	}
}

func ptyPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("a PTY is required: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Fatal(errno)
	}
	fd, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x5441, uintptr(syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC)) // TIOCGPTPEER
	if errno != 0 {
		t.Fatal(errno)
	}
	slave := os.NewFile(fd, "pty")
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

func TestVerifyDescriptorsRefusesWrongKinds(t *testing.T) {
	uid := uint32(os.Getuid())
	p := pipes(t, 3) // r0 w0 r1 w1 r2 w2
	master, slave := ptyPair(t)
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	readOnlySlave, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(int(slave.Fd())), os.O_RDONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnlySlave.Close()
	a, b := socketPair(t, syscall.SOCK_STREAM)
	defer syscall.Close(b)
	sock := os.NewFile(uintptr(a), "socket")
	defer sock.Close()
	if err := VerifyDescriptors([]*os.File{p[0], p[3], p[5]}, false, uid); err != nil {
		t.Fatalf("pipes: %v", err)
	}
	if err := VerifyDescriptors([]*os.File{slave}, true, uid); err != nil {
		t.Fatalf("pty: %v", err)
	}
	for name, c := range map[string]struct {
		fds []*os.File
		pty bool
		uid uint32
	}{
		"null as pty":      {[]*os.File{null}, true, uid},
		"master as pty":    {[]*os.File{master}, true, uid},
		"read-only pty":    {[]*os.File{readOnlySlave}, true, uid},
		"pipe as pty":      {[]*os.File{p[1]}, true, uid},
		"two for pty":      {[]*os.File{slave, slave}, true, uid},
		"pty as pipes":     {[]*os.File{slave, slave, slave}, false, uid},
		"reversed ends":    {[]*os.File{p[1], p[0], p[3]}, false, uid},
		"write stdin":      {[]*os.File{p[1], p[3], p[5]}, false, uid},
		"read stderr":      {[]*os.File{p[0], p[3], p[4]}, false, uid},
		"socket":           {[]*os.File{sock, p[3], p[5]}, false, uid},
		"null stdout":      {[]*os.File{p[0], null, p[5]}, false, uid},
		"two pipes":        {[]*os.File{p[0], p[3]}, false, uid},
		"other owner":      {[]*os.File{p[0], p[3], p[5]}, false, uid + 1},
		"other owner pty":  {[]*os.File{slave}, true, uid + 1},
		"nil":              {[]*os.File{nil, p[3], p[5]}, false, uid},
		"four descriptors": {[]*os.File{p[0], p[3], p[5], p[1]}, false, uid},
	} {
		if err := VerifyDescriptors(c.fds, c.pty, c.uid); !errors.Is(err, ErrSession) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}
