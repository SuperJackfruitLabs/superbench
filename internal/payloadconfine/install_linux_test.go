//go:build linux

package payloadconfine

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// The child modes run in a re-executed test binary, so the filter never
// reaches the test runner itself. They need no privilege.
func TestMain(m *testing.M) {
	switch os.Getenv("SB_CONFINE_CHILD") {
	case "":
		os.Exit(m.Run())
	case "without-no-new-privs":
		if set, _, _ := syscall.RawSyscall(syscall.SYS_PRCTL, 39, 0, 0); set == 1 {
			os.Stdout.WriteString("INHERITED\n")
			os.Exit(0)
		}
		if err := Install(); !errors.Is(err, ErrConfine) || Verify() == nil {
			fail("installed without no_new_privs", err)
		}
		os.Stdout.WriteString("REFUSED\n")
	case "install":
		if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 38, 1, 0); e != 0 {
			fail("no_new_privs", e)
		}
		if err := Install(); err != nil {
			fail("install", err)
		}
		probe("installed")
		// Exec keeps the filter: the same probes refuse in the new image.
		env := []string{"SB_CONFINE_CHILD=after-exec"}
		for _, item := range os.Environ() {
			if !strings.HasPrefix(item, "SB_CONFINE_CHILD=") {
				env = append(env, item)
			}
		}
		err := syscall.Exec("/proc/self/exe", []string{os.Args[0], "-test.run=^$"}, env)
		fail("exec", err)
	case "after-exec":
		if Verify() != nil {
			fail("filter lost across exec", nil)
		}
		probe("after-exec")
	}
	os.Exit(0)
}

func fail(what string, err error) {
	os.Stderr.WriteString(what + ": " + errString(err) + "\n")
	os.Exit(3)
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// probe proves every denied route fails with EPERM, and that a read-only
// attribute ioctl still reaches the filesystem.
func probe(stage string) {
	f, err := os.CreateTemp("", "confine")
	if err != nil {
		fail(stage+" temp", err)
	}
	defer os.Remove(f.Name())
	var attr [128]byte // large enough for io_uring_params and fsxattr
	for _, cmd := range DeniedIoctls {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(cmd), uintptr(unsafe.Pointer(&attr[0]))); e != syscall.EPERM {
			fail(stage+" ioctl not refused", e)
		}
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(0xffffffff00000000|uint64(cmd)), uintptr(unsafe.Pointer(&attr[0]))); e != syscall.EPERM {
			fail(stage+" high-bit ioctl not refused", e)
		}
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0x801c581f, uintptr(unsafe.Pointer(&attr[0]))); e == syscall.EPERM {
		fail(stage+" FS_IOC_FSGETXATTR refused", e)
	}
	for _, nr := range DeniedSyscalls {
		if nr >= 425 && nr <= 427 && os.Getenv("SB_CONFINE_IO_URING_DISABLED") == "1" {
			continue // the host refuses io_uring with EPERM already: unprovable here
		}
		if _, _, e := syscall.Syscall6(uintptr(nr), 1, uintptr(unsafe.Pointer(&attr[0])), 0, 0, 0, 0); e != syscall.EPERM {
			fail(stage+" syscall not refused", e)
		}
	}
	os.Stdout.WriteString(stage + " REFUSED\n")
}

func child(t *testing.T, mode string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "SB_CONFINE_CHILD="+mode)
	// With io_uring_disabled set, the host already refuses io_uring with
	// EPERM, so the filter's refusal cannot be told apart and is not probed.
	if raw, err := os.ReadFile("/proc/sys/kernel/io_uring_disabled"); err == nil && strings.TrimSpace(string(raw)) != "0" {
		t.Logf("io_uring_disabled=%s on this host: the io_uring refusal is not proven here, only the attribute ioctls and file_setattr", strings.TrimSpace(string(raw)))
		cmd.Env = append(cmd.Env, "SB_CONFINE_IO_URING_DISABLED=1")
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s child: %v %s", mode, err, out)
	}
	return string(out)
}

func TestInstallRequiresNoNewPrivs(t *testing.T) {
	out := child(t, "without-no-new-privs")
	if out == "INHERITED\n" {
		t.Skip("the test runner already has no_new_privs")
	}
	if out != "REFUSED\n" {
		t.Fatalf("%q", out)
	}
}

func TestInstalledFilterRefusesAndSurvivesExec(t *testing.T) {
	if out := child(t, "install"); !strings.Contains(out, "installed REFUSED\n") || !strings.Contains(out, "after-exec REFUSED\n") {
		t.Fatalf("%q", out)
	}
}
