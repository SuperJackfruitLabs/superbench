//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadsession"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
)

// Session mode (fd 0 is a socket) and its PID 1 stage, which only Session starts.
var SessionStdin, SessionChild = payloadsession.Stdin, payloadsession.Child

// Session is the second fixed mode, with Exec's contract (exit on any error): one
// of three fixed argvs as the payload user, in the bench's own mount namespace.
func Session() (err error) {
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
	if err := payloadsession.VerifyStdio(p.DaemonUID); err != nil {
		return err
	}
	phase = "descriptors"
	if err := descriptorsCloseOnExec(); err != nil {
		return err
	}
	phase = "frame"
	s, err := payloadwire.ReadSessionFrame(os.Stdin, frameTimeout)
	if err != nil {
		return err
	}
	path := map[string]string{"shell": "/usr/bin/bash", "exec": "/usr/bin/bash", "sftp": "/usr/local/libexec/bench-sftp"}[s.Mode]
	argv := map[string][]string{"shell": {"bash", "-l"}, "exec": {"bash", "-c", s.Command}, "sftp": {"bench-sftp"}}[s.Mode]
	r := Request{Version: Version, Kind: "payload", Root: true, Key: s.Key, LayoutID: s.LayoutID, PolicyDigest: s.PolicyDigest, Scope: s.Scope, Arguments: argv}
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
	phase = "descriptors"
	pty := s.Term != ""
	fds, err := payloadsession.Receive(0, map[bool]int{true: 1, false: 3}[pty], frameTimeout)
	if err == nil {
		err = payloadsession.VerifyDescriptors(fds, pty, p.DaemonUID)
	}
	if err != nil {
		return err
	}
	// StateRoot is hidden in the namespace: recheck the registration before it.
	phase = "layout"
	if current, err := LoadLayout(p, r); err != nil || !reflect.DeepEqual(current, l) {
		return errors.Join(ErrBoundary, err)
	}
	phase = "boundary"
	if err := payloadsession.Enter(p, s.Key, b.cwd); err != nil {
		return err
	} else if err := b.check(); err != nil {
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
	phase = "environment"
	_, _, home, tmp, err := l.paths(p, "")
	if err != nil {
		return err
	}
	ambient := slices.DeleteFunc(os.Environ(), func(item string) bool { return strings.HasPrefix(item, "TERM=") })
	if pty {
		ambient = append(ambient, "TERM="+s.Term)
	}
	environment, err := Environment(home, tmp, ambient)
	if err != nil {
		return err
	}
	phase = "executable"
	if err := verifyFixedExecutable(path); err != nil {
		return err
	}
	// Nothing privileged crosses the fork, and the root stage keeps nothing.
	phase = "exec"
	release := func() error { _ = scope.Close(); b.close(); return descriptorsCloseOnExec() }
	h := payloadsession.Handoff{Path: path, Argv: argv, Env: environment, PTY: pty, Directory: filepath.Join(p.DataRoot, "benches", s.Key)}
	return payloadsession.Run(fds, b.cwd, h, p.PayloadUID, p.PayloadGID, release)
}

// SessionInit is the session's PID 1: a private /proc, the drop, the target.
func SessionInit() (err error) {
	phase := "caller"
	defer refuse(&phase, &err)
	if os.Getuid() != 0 || os.Geteuid() != 0 || !SessionChild() {
		return ErrBoundary
	}
	phase = "policy"
	binding, err := loadHelperBinding()
	if err != nil {
		return err
	}
	phase = "boundary"
	h, err := payloadsession.Begin()
	if err != nil {
		return err
	}
	phase = "credentials"
	if err := dropCredentials(binding.policy); err != nil {
		return err
	}
	phase = "exec"
	return payloadsession.Init(h, func() error { return errors.Join(verifyDropped(binding.policy), descriptorsCloseOnExec()) })
}
