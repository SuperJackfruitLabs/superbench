package workspaceprep_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os/exec"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

// finitePlan replaces the helper with a plain pipe: it proves lifecycle joining
// only. Sealed delivery after credential drop is the privileged fixture's job.
type finitePlan struct {
	limit, checks, refuseAt int
}
type pipeTransport struct{ cmd *exec.Cmd }
type fencer = interface {
	Fence(*execution.Execution) error
}

func (p *finitePlan) Check() error {
	if p.checks++; p.checks == p.refuseAt {
		return errors.New("installed identity changed")
	}
	return nil
}
func (p *finitePlan) io() finiteio.Input {
	return finiteio.Input{Version: finiteio.Version, StdinBase64: "AAEC", StdoutLimitBytes: uint64(p.limit)}
}
func (p *finitePlan) Limit() uint64                             { return p.io().StdoutLimitBytes }
func (p *finitePlan) Observe(b []byte) (finiteio.Output, error) { return finiteio.Observe(b, p.io()) }
func (p *finitePlan) Transport(*model.Bench, string) (stageexec.PrepareTransport, error) {
	return func(cmd *exec.Cmd, _ *execution.Execution) (stageexec.Transport, error) {
		cmd.Stdin = bytes.NewReader([]byte{0, 1, 2})
		return pipeTransport{cmd}, nil
	}, nil
}
func (pipeTransport) Check() error                        { return nil }
func (pipeTransport) StartIO() error                      { return nil }
func (t pipeTransport) WaitLeader() error                 { return t.cmd.Wait() }
func (pipeTransport) Failures() <-chan error              { return nil }
func (pipeTransport) Close() error                        { return nil }
func (pipeTransport) Drain(context.Context, fencer) error { return nil }

func TestFiniteRunPublishesOnlySealedStdoutOfItsOwnStoppedExecution(t *testing.T) {
	for name, c := range map[string]struct {
		script, want              string
		limit, refuseAt, launches int
	}{"success": {"cat; printf -; printf noise >&2", "\x00\x01\x02-", 8, 0, 2}, "nonzero-exit": {"cat; exit 7", "\x00\x01\x02", 8, 0, 2},
		"overflow": {"cat; printf more", "", 4, 0, 2}, "identity-changed-at-launch": {"cat", "", 8, 2, 1}} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 1, nil)
			f.ready()
			plan := &finitePlan{limit: c.limit, refuseAt: c.refuseAt}
			_, before := f.backend.counts()
			result, err := f.manager.Run(context.Background(), "card", f.initial.Lease.Token, "", []string{"/bin/sh", "-c", c.script}, false, plan)
			if _, after := f.backend.counts(); after-before != c.launches || result == nil || (c.want == "") != (result.IO == nil) {
				t.Fatalf("launches %d want %d, io %+v: %v", after-before, c.launches, result, err)
			}
			// launches include the member's preparation hook; only success has no error.
			if (err == nil) != (name == "success") || c.want != "" && (result.IO.StdoutBase64 != base64.StdEncoding.EncodeToString([]byte(c.want)) || plan.checks < 3) {
				t.Fatalf("finite observation: %+v %v checks=%d", result.IO, err, plan.checks)
			}
		})
	}
}
