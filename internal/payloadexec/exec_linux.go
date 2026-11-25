//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

// Exec belongs only in a dedicated fixed helper. The caller must exit on every
// returned error: credentials may already have been irreversibly dropped. This
// function must never be called inside a daemon or reusable server process.
// Only completed read-only layout or preparation-input inspections return nil.
// It consumes a bounded request frame from stdin, retaining only Git's subsequent
// ordinary stdin. It does not create or convert any layout or change ownership.
func Exec() (err error) {
	phase := "caller"
	defer refuse(&phase, &err)
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return ErrBoundary
	}
	phase = "policy"
	binding, err := loadHelperBinding()
	if err != nil {
		return err
	}
	p := binding.policy
	phase = "stdio"
	if err := verifyStdio(p); err != nil {
		return err
	}
	phase = "descriptors"
	if err := descriptorsCloseOnExec(); err != nil {
		return err
	}
	phase = "frame"
	inspection, preparation, r, err := readAnyInvocation(os.Stdin, frameTimeout)
	if err != nil {
		return err
	}
	if inspection != nil {
		phase = "inspection"
		return inspectInstalled(binding, *inspection)
	}
	if preparation != nil {
		phase = "preparation"
		return inspectPreparation(binding, *preparation)
	}
	phase = "scope"
	scope, err := VerifyScope(p, r)
	if err != nil {
		return err
	}
	defer scope.Close()
	phase = "layout"
	l, err := LoadLayout(p, r)
	if err != nil {
		return err
	}
	phase = "boundary"
	b, err := openBoundary(p, r, l)
	if err != nil {
		return err
	}
	defer b.close()
	_, _, home, tmp, err := l.paths(p, r.Member)
	if err != nil {
		return err
	}
	phase = "environment"
	environment, err := Environment(home, tmp, os.Environ())
	if err != nil {
		return err
	}
	if r.Kind == "git" {
		environment = append(environment, "GIT_TERMINAL_PROMPT=0", "GIT_ATTR_NOSYSTEM=1")
	}
	phase = "executable"
	if err := verifyCommandDirectories(); err != nil {
		return err
	}
	if r.Kind == "git" {
		if err := verifyFixedGit(); err != nil {
			return err
		}
	}
	// The framed input and immutable registration must still name the same
	// scope/layout at the last privileged boundary. No SQL or callbacks run here.
	phase = "layout"
	current, err := LoadLayout(p, r)
	if err != nil || !reflect.DeepEqual(current, l) {
		return errors.Join(ErrBoundary, err)
	}
	phase = "boundary"
	if err := b.check(); err != nil {
		return err
	}
	phase = "scope"
	if err := recheckScope(p, r, scope); err != nil {
		return err
	}
	phase = "policy"
	if err := binding.recheck(); err != nil {
		return err
	}
	phase = "stdio"
	if err := verifyStdio(p); err != nil {
		return err
	}
	phase = "descriptors"
	if err := descriptorsCloseOnExec(); err != nil {
		return err
	}
	phase = "input"
	input, err := openPayloadInput(r)
	if err != nil {
		return err
	}
	if input != nil {
		defer input.Close()
	}
	phase = "credentials"
	if err := dropCredentials(p); err != nil {
		return err
	}
	phase = "boundary"
	if err := syscall.Fchdir(int(b.cwd.Fd())); err != nil {
		return errors.Join(ErrBoundary, err)
	}
	// Lookup is deliberately after the credential drop. It uses a reconstructed
	// fixed PATH; neither daemon secrets nor loader/sudo/Git overrides survive.
	phase = "environment"
	os.Clearenv()
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		if err := os.Setenv(key, value); err != nil {
			return errors.Join(ErrBoundary, err)
		}
	}
	phase = "executable"
	path := "/usr/bin/git"
	if r.Kind != "git" {
		path, err = exec.LookPath(r.Arguments[0])
		if err != nil {
			return errors.Join(ErrBoundary, err)
		}
	}
	if r.Kind == "git" {
		phase = "git-policy"
		if err := verifyManagedGit(r, b.admin, environment); err != nil {
			return err
		}
	}
	return enterPayload(&phase, p, r, input, path, environment)
}

// enterPayload is the only place the final launch order lives, shared by Exec
// and the privileged fixture. It requires the actual credential drop to be
// complete, marks every other descriptor close-on-exec, binds the sealed input
// to stdin once and execs. With ReportExec it writes ExecReached to stdout
// immediately before exec, and ExecFailed if exec returns: a helper or sudo
// refusal never produces ExecReached, and a payload cannot write before it.
func enterPayload(phase *string, p Policy, r Request, input *os.File, path string, environment []string) error {
	*phase = "credentials"
	if err := verifyDropped(p); err != nil {
		return err
	}
	*phase = "descriptors"
	if err := descriptorsCloseOnExec(); err != nil {
		return err
	}
	*phase = "input"
	if input != nil {
		if err := bindPayloadInput(input); err != nil {
			return err
		}
	}
	*phase = "exec"
	if r.ReportExec {
		if err := writeStdout(ExecReached); err != nil {
			return err
		}
	}
	err := syscall.Exec(path, r.Arguments, environment)
	if r.ReportExec {
		_ = writeStdout(ExecFailed)
	}
	return err
}
func writeStdout(marker string) error { return writeFD(1, marker) }
func writeFD(fd int, marker string) error {
	for raw := []byte(marker); len(raw) > 0; {
		n, err := syscall.Write(fd, raw)
		if err != nil {
			return errors.Join(ErrBoundary, err)
		}
		raw = raw[n:]
	}
	return nil
}

func verifyCommandDirectories() error {
	for _, path := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		// Fixed operating-system symlinks such as /bin -> /usr/bin are allowed
		// only beneath a physical root-owned parent and to a protected target.
		parent, err := openPhysicalDirectory(filepath.Dir(path), 0)
		if err != nil {
			return errors.Join(ErrBoundary, err)
		}
		_ = parent.Close()
		leaf, err := os.Lstat(path)
		if err != nil || leaf.Sys().(*syscall.Stat_t).Uid != 0 {
			return errors.Join(ErrBoundary, err)
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil {
			return errors.Join(ErrBoundary, err)
		}
		directory, err := openPhysicalDirectory(canonical, 0)
		if err != nil {
			return errors.Join(ErrBoundary, err)
		}
		_ = directory.Close()
	}
	return nil
}

func verifyFixedGit() error { return verifyFixedExecutable("/usr/bin/git") }
func verifyFixedExecutable(path string) error {
	parent, err := openPhysicalDirectory(filepath.Dir(path), 0)
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), filepath.Base(path), syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	file := os.NewFile(uintptr(fd), "trusted executable")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return errors.Join(ErrBoundary, err)
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return ErrBoundary
	}
	return nil
}
