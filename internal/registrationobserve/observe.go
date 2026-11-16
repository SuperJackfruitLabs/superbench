// Package registrationobserve joins an existing held workspace owner to one
// installed, physically inspected registration. It cannot create a lease,
// registration, process scope, checkpoint or permission transition.
package registrationobserve

import (
	"context"
	"errors"
	"sync"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

var ErrObservation = errors.New("private registration observation refused")

// Request is an internal owner capability, never a public reader projection.
// Callers must authenticate the actual operator before invoking Observe.
type Request struct{ Key, BenchID, Holder, LeaseToken string }

// Result is observed metadata, not a grant, execution scope or fencing proof.
// Checkpoint is the original complete durable checkpoint, not a Git refresh.
type Result struct {
	Layout     payloadexec.Layout
	RootPath   string
	Checkpoint model.WorkspaceCheckpoint
}

// Observation retains original shared exclusion through response publication.
// Its private closures are established only from actual authority in Observe.
type Observation struct {
	mu     sync.Mutex
	result Result
	check  func() error
	close  func()
	cancel context.CancelFunc
}

func (o *Observation) Result() (Result, error) {
	if o == nil {
		return Result{}, ErrObservation
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.check == nil || o.check() != nil {
		return Result{}, ErrObservation
	}
	r := o.result
	r.Layout.Members = append([]payloadexec.LayoutMember(nil), r.Layout.Members...)
	r.Checkpoint.Members = append([]model.WorkspaceCommit(nil), r.Checkpoint.Members...)
	return r, nil
}
func (o *Observation) Close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.close != nil {
		o.close()
		o.close = nil
	}
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
	o.check = nil
	o.result = Result{}
}
