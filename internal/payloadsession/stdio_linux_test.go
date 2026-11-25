//go:build linux

package payloadsession

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestVerifyStdioRefusesPipeStdin(t *testing.T) {
	uid := uint32(os.Getuid())
	p := pipes(t, 2) // r0 w0 r1 w1
	a, b := socketPair(t, syscall.SOCK_STREAM)
	defer syscall.Close(a)
	defer syscall.Close(b)
	d, e := socketPair(t, syscall.SOCK_DGRAM)
	defer syscall.Close(d)
	defer syscall.Close(e)
	in, out, errOut := b, int(p[1].Fd()), int(p[3].Fd())
	if err := stdio(in, out, errOut, uid); err != nil {
		t.Fatalf("socket stdin, pipe stdout and stderr: %v", err)
	}
	for name, c := range map[string][4]int{
		"pipe stdin":      {int(p[0].Fd()), out, errOut, int(uid)},
		"datagram stdin":  {e, out, errOut, int(uid)},
		"read stdout":     {in, int(p[0].Fd()), errOut, int(uid)},
		"read stderr":     {in, out, int(p[2].Fd()), int(uid)},
		"socket stdout":   {in, a, errOut, int(uid)},
		"other daemon":    {in, out, errOut, int(uid) + 1},
		"closed stdin":    {1 << 20, out, errOut, int(uid)},
		"closed stderr":   {in, out, 1 << 20, int(uid)},
		"stdout is stdin": {in, in, errOut, int(uid)},
	} {
		if err := stdio(c[0], c[1], c[2], uint32(c[3])); !errors.Is(err, ErrSession) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	// The exported check reads the process's own fd 0: a pipe there refuses.
	saved, err := syscall.Dup(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(p[0].Fd()), 0, 0); err != nil {
		t.Fatal(err)
	}
	refused := VerifyStdio(uid)
	if err := syscall.Dup3(saved, 0, 0); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Close(saved)
	if !errors.Is(refused, ErrSession) {
		t.Fatalf("pipe stdin accepted: %v", refused)
	}
}
