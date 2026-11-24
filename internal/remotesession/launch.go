// Package remotesession is benchd's side of bench-payload's session mode: it
// frames one admitted session, starts the fixed helper through sudo inside the
// lease's remote scope and hands it the session's descriptors.
package remotesession

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotessh"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remotestate"
)

// ErrRefused wraps the helper's static refusal code, or "unknown" when its
// stderr held none (a sudo refusal, for example). No session started.
var ErrRefused = errors.New("remote session refused")

// ErrSpec is a session the launcher will not frame at all.
var ErrSpec = errors.New("invalid remote session")

// StartTimeout bounds the handshake: frame, descriptors and ExecReached.
const StartTimeout = 30 * time.Second

// Installed launches through the installed helper. It resolves the installed
// service identity on every Start; no field or option selects another helper.
type Installed struct{}

// sessionRequest frames a for the helper. Scope is the lease's remote
// execution: the helper verifies it is already inside that cgroup.
func sessionRequest(a remotestate.Admission, spec remotessh.SessionSpec, policyDigest string) (payloadwire.SessionRequest, error) {
	e := a.Execution
	if spec.PTY != (spec.Term != "") || e.Kind != execution.KindRemote || e.Phase != "running" {
		return payloadwire.SessionRequest{}, ErrSpec
	}
	r := payloadwire.SessionRequest{Version: payloadwire.SessionVersion, Key: a.Key, LayoutID: a.LayoutID, PolicyDigest: policyDigest, Scope: payloadwire.Scope{ID: e.ID, HostID: e.HostID, BootID: e.BootID}, Mode: spec.Mode, Command: spec.Command, Term: spec.Term}
	if err := r.Validate(); err != nil {
		return payloadwire.SessionRequest{}, errors.Join(ErrSpec, err)
	}
	return r, nil
}

var refusalCode = regexp.MustCompile(`^[a-z-]{1,32}$`)

// refusal keeps only the helper's static code from at most 4 KiB of stderr.
func refusal(stderr []byte) error {
	code := "unknown"
	if line, ok := bytes.CutPrefix(bytes.TrimSpace(stderr), []byte("managed payload refused: ")); ok && refusalCode.Match(line) {
		code = string(line)
	}
	return fmt.Errorf("%w: %s", ErrRefused, code)
}

// handshake writes the frame, then 'F' carrying fds, then end of stream, and
// waits for ExecReached. EOF or anything else first is a refusal; ctx and the
// deadline shut the socket down. fds are never closed here.
func handshake(ctx context.Context, control int, body []byte, fds []*os.File, deadline time.Time) error {
	// The shutdown never outlives this call, so it cannot hit a reused fd.
	var mu sync.Mutex
	finished := false
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			_ = syscall.Shutdown(control, syscall.SHUT_RDWR)
		}
	})
	defer func() {
		stop()
		mu.Lock()
		finished = true
		mu.Unlock()
	}()
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	tv := syscall.NsecToTimeval(remaining.Nanoseconds())
	for _, option := range []int{syscall.SO_SNDTIMEO, syscall.SO_RCVTIMEO} {
		if err := syscall.SetsockoptTimeval(control, syscall.SOL_SOCKET, option, &tv); err != nil {
			return err
		}
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	if err := writeAll(control, append(frame, body...)); err != nil {
		return errors.Join(ErrRefused, err)
	}
	descriptors := make([]int, len(fds))
	for i, f := range fds {
		descriptors[i] = int(f.Fd())
	}
	if err := syscall.Sendmsg(control, []byte{'F'}, syscall.UnixRights(descriptors...), nil, 0); err != nil {
		return errors.Join(ErrRefused, err)
	}
	if err := syscall.Shutdown(control, syscall.SHUT_WR); err != nil {
		return errors.Join(ErrRefused, err)
	}
	marker := make([]byte, len(payloadwire.ExecReached))
	for read := 0; read < len(marker); {
		n, err := syscall.Read(control, marker[read:])
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return errors.Join(ErrRefused, err, ctx.Err())
		}
		read += n
	}
	if string(marker) != payloadwire.ExecReached {
		return ErrRefused
	}
	return nil
}

func writeAll(fd int, raw []byte) error {
	for len(raw) > 0 {
		n, err := syscall.Write(fd, raw)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		raw = raw[n:]
	}
	return nil
}

func closeAll(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
