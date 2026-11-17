package payloadstage

import (
	"bytes"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

// ErrIncomplete refuses an observation that is not bound to the exact stopped,
// drained and sealed execution its caller launched and still owns.
var ErrIncomplete = errors.New("finite payload output requires its stopped, drained and sealed execution")

// FiniteRun is one profiled run's copied input and requested stdout bound. It
// carries no lease, scope, registration or preparation authority. Check must
// revalidate the installed identity at each gate and before publication.
// Transport must deliver the input only through the fixed helper: a sealed
// memfd bound to stdin once, after the actual credential drop.
type FiniteRun interface {
	Check() error
	Limit() uint64
	Transport(b *model.Bench, selector string) (stageexec.PrepareTransport, error)
	Observe(stdout []byte) (finiteio.Output, error)
}

// Check is a nil-tolerant gate: no plan means the unchanged unprofiled run.
func Check(f FiniteRun) error {
	if f == nil {
		return nil
	}
	return f.Check()
}

// Transport and Limit are nil-tolerant: no plan keeps the legacy runner.
func Transport(f FiniteRun, b *model.Bench, selector string) (stageexec.PrepareTransport, error) {
	if f == nil {
		return nil, nil
	}
	return f.Transport(b, selector)
}
func Limit(f FiniteRun) uint64 {
	if f == nil {
		return 0
	}
	return f.Limit()
}

// Installed is the production FiniteRun over a resolved installed identity.
type Installed struct {
	authority serviceidentity.Authority
	policy    payloadpolicy.Policy
	input     finiteio.Input
	bytes     []byte
}

// NewInstalled copies validated input and resolves the installed service. A
// host without the complete installed binding refuses; there is no fallback.
func NewInstalled(input finiteio.Input) (*Installed, error) {
	bytes, err := input.Bytes()
	if err != nil {
		return nil, err
	}
	authority, err := serviceidentity.ResolveService()
	if err != nil {
		return nil, err
	}
	return newInstalled(authority, input, bytes)
}
func newInstalled(authority serviceidentity.Authority, input finiteio.Input, bytes []byte) (*Installed, error) {
	policy, err := authority.Policy()
	if err != nil {
		return nil, err
	}
	return &Installed{authority: authority, policy: policy, input: input, bytes: append([]byte{}, bytes...)}, nil
}
func (f *Installed) Check() error {
	if f == nil {
		return serviceidentity.ErrAuthority
	}
	policy, err := f.authority.Policy()
	if err != nil {
		return err
	}
	if policy != f.policy {
		return ErrRegistration
	}
	return nil
}

// Limit allows the helper's fixed ExecReached prefix on top of the request's
// stdout bound; Observe strips and requires it before applying that bound.
func (f *Installed) Limit() uint64 {
	return f.input.StdoutLimitBytes + uint64(len(payloadexec.ExecReached))
}
func (f *Installed) Transport(b *model.Bench, selector string) (stageexec.PrepareTransport, error) {
	if err := f.Check(); err != nil {
		return nil, err
	}
	return workspaceTransport(f.authority, b, selector, selector == "", f.bytes, true)
}
func (f *Installed) Observe(stdout []byte) (finiteio.Output, error) {
	payload, err := helperStdout(stdout)
	if err != nil {
		return finiteio.Output{}, err
	}
	return finiteio.Observe(payload, f.input)
}

// helperStdout returns the payload's own stdout only when the helper proved
// that exec was reached. A helper or sudo refusal (any exit status, empty or
// diagnostic-free stdout) and a failed exec never become an observation.
func helperStdout(raw []byte) ([]byte, error) {
	payload, reached := bytes.CutPrefix(raw, []byte(payloadexec.ExecReached))
	if !reached || string(payload) == payloadexec.ExecFailed {
		return nil, ErrIncomplete
	}
	return payload, nil
}

// Complete publishes an observation only for the exact execution the caller
// launched, now stopped in both the runner result and current ownership, whose
// stdout stageexec captured after fence, drain and seal with a known exit.
func Complete(f FiniteRun, launched *execution.Execution, out *stageexec.Result, current *model.Bench) (*finiteio.Output, error) {
	if f == nil || launched == nil || out == nil || out.Output == nil || out.Execution == nil || out.ExitCode < 0 {
		return nil, ErrIncomplete
	}
	if out.Execution.ID != launched.ID || out.Execution.Phase != "stopped" || current == nil || current.Lease == nil || current.Lease.Execution == nil || current.Lease.Execution.ID != launched.ID || current.Lease.Execution.Phase != "stopped" {
		return nil, ErrIncomplete
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	observed, err := f.Observe(out.Output)
	if err != nil {
		return nil, err
	}
	return &observed, nil
}
