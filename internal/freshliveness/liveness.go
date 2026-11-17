// Package freshliveness is root's own view of whether anything still uses a
// Fresh bench, read from the cgroup and proc filesystems, and the watchdog that
// kills a purge worker the payload account has stopped. It holds no storage,
// ledger or journal authority; only internal/freshrecovery imports it.
package freshliveness

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	// ErrBusy refuses a reclaim while anything still uses the bench:
	// a populated cgroup scope holding a process with its working directory,
	// root or an open file in the bench, or such a process of the payload
	// account anywhere. Root checks this itself, before it retains the
	// reclaiming intent and again before the purge; it does not trust the
	// daemon's record that the bench is released.
	ErrBusy = errors.New("fresh reclaim refused: the bench is in use; stop its runs and retry the same reclaim")
	// ErrPayloadActive reports a reclaim stopped because the payload
	// account interfered: the purge worker made no progress (it was stopped),
	// or entries or usage reappeared after it. The reclaim is retained.
	ErrPayloadActive = errors.New("fresh reclaim stopped: the payload account is still active in the bench; stop it and retry the same reclaim")
)

// ErrOversized refuses a worker reply over its bound.
var ErrOversized = errors.New("clean worker reply exceeds its bound")

// PurgeIdle is how long a purge worker may report no progress before root
// kills it. The worker reports at least once a second while it works.
const PurgeIdle = 30 * time.Second

// Unpopulated parses cgroup.events: exactly one "populated" key whose value is
// 0, whatever the surrounding whitespace.
func Unpopulated(events []byte) bool {
	found := 0
	for _, line := range strings.Split(string(events), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "populated" {
			continue
		}
		if len(f) != 2 || f[1] != "0" {
			return false
		}
		found++
	}
	return found == 1
}

// Liveness is how root sees who uses a bench, read from the cgroup and proc
// filesystems by the Linux adapter and stubbed by tests.
type Liveness struct {
	// Scopes lists the cgroup scopes beneath the policy's cgroup root; none
	// when the root is absent.
	Scopes func() ([]string, error)
	// Events and Members read one scope's cgroup.events and cgroup.procs.
	Events  func(scope string) ([]byte, error)
	Members func(scope string) ([]int, error)
	// Processes lists every process; Payload reports whether one runs as the
	// payload account in any credential slot. A vanished process is false.
	Processes func() ([]int, error)
	Payload   func(pid int) (bool, error)
	// Uses reports whether a process has its working directory, root or an
	// open file in the bench. A vanished process is false.
	Uses func(pid int) (bool, error)
}

// Idle refuses unless every scope is unpopulated or holds no process that
// uses the bench, and no payload process anywhere uses it.
func (l Liveness) Idle() error {
	scopes, err := l.Scopes()
	if err != nil {
		return err
	}
	check := func(pid int) error {
		using, err := l.Uses(pid)
		if err != nil {
			return err
		}
		if using {
			return fmt.Errorf("%w: process %d", ErrBusy, pid)
		}
		return nil
	}
	for _, scope := range scopes {
		events, err := l.Events(scope)
		if err != nil {
			return err
		}
		if Unpopulated(events) {
			continue
		}
		pids, err := l.Members(scope)
		if err != nil {
			return err
		}
		for _, pid := range pids {
			if err = check(pid); err != nil {
				return err
			}
		}
	}
	pids, err := l.Processes()
	if err != nil {
		return err
	}
	for _, pid := range pids {
		payload, err := l.Payload(pid)
		if err != nil {
			return err
		}
		if payload {
			if err = check(pid); err != nil {
				return err
			}
		}
	}
	return nil
}

// WatchedRead reads a purge worker's stdout: progress bytes ('.') and then its
// reply frame. It kills the worker and refuses when no byte arrives for idle,
// so a worker the payload stopped cannot hold root's locks.
func WatchedRead(r io.Reader, idle time.Duration, kill func() error, limit int) ([]byte, error) {
	type chunk struct {
		raw []byte
		err error
	}
	chunks := make(chan chunk, 1)
	go func() {
		for {
			buf := make([]byte, 4096)
			n, err := r.Read(buf)
			chunks <- chunk{buf[:n], err}
			if err != nil {
				return
			}
		}
	}()
	var out []byte
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case c := <-chunks:
			raw := c.raw
			if len(out) == 0 {
				raw = []byte(strings.TrimLeft(string(raw), "."))
			}
			out = append(out, raw...)
			if len(out) > limit {
				_ = kill()
				return nil, ErrOversized
			}
			if errors.Is(c.err, io.EOF) {
				return out, nil
			}
			if c.err != nil {
				return nil, c.err
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(idle)
		case <-timer.C:
			// The kill closes the worker's stdout, which ends the reader.
			if err := kill(); err != nil {
				return nil, errors.Join(ErrPayloadActive, err)
			}
			go func() {
				for c := range chunks {
					if c.err != nil {
						return
					}
				}
			}()
			return nil, ErrPayloadActive
		}
	}
}
