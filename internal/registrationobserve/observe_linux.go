//go:build linux

package registrationobserve

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/localprep"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

var generation = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hexCommit = regexp.MustCompile(`^[0-9a-f]+$`)

func commit(value string) bool {
	return (len(value) == 40 || len(value) == 64) && hexCommit.MatchString(value)
}

// Observe is deliberately not installed in a service router. Its caller must
// already be authenticated as an operator. The actual installed Authority and
// retained existing host lock remain mandatory independently of that caller.
func Observe(ctx context.Context, store bench.Store, authority serviceidentity.Authority, r Request) (_ *Observation, err error) {
	var zero *Observation
	if ctx == nil || ctx.Err() != nil || r.Holder == "" || len(r.Holder) > 200 || !utf8.ValidString(r.Holder) || r.LeaseToken == "" || len(r.LeaseToken) > 200 {
		return zero, ErrObservation
	}
	policy, err := authority.Policy()
	if err != nil || store.State != policy.StateRoot || store.Data != policy.DataRoot || store.CgroupRoot != policy.CgroupRoot {
		return zero, ErrObservation
	}
	registry, err := serviceidentity.ForPolicy(policy)
	if err != nil {
		return zero, ErrObservation
	}
	request := payloadexec.InspectionRequest{Version: payloadexec.InspectionVersion, Key: r.Key, LayoutID: r.BenchID, PolicyDigest: registry.PolicySHA256}
	if request.Validate() != nil {
		return zero, ErrObservation
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	held, err := holdState(ctx, store.State)
	if err != nil {
		return zero, err
	}
	defer func() {
		if err != nil {
			held.close()
		}
	}()
	// Each read below also takes the existing shared host lock. The independent
	// original descriptor above closes the missing-lock/replacement fallback.
	read := func() (model.Bench, error) {
		if err := held.recheck(); err != nil {
			return model.Bench{}, err
		}
		if err := store.CheckWorkspaceOwner(r.Key, r.LeaseToken, r.Holder); err != nil {
			return model.Bench{}, errors.Join(ErrObservation, err)
		}
		rows, err := store.Snapshot(r.Key)
		if err != nil || len(rows) != 1 {
			return model.Bench{}, ErrObservation
		}
		b := rows[0]
		if err = ready(policy, r, b); err != nil {
			return model.Bench{}, err
		}
		if err = localprep.CheckHandoff(store.State, &b); err != nil {
			return model.Bench{}, errors.Join(ErrObservation, err)
		}
		return b, nil
	}
	before, err := read()
	if err != nil {
		return zero, err
	}
	layout, err := inspect(ctx, policy, request)
	if err != nil {
		return zero, ErrObservation
	}
	if err = joinLayout(policy, r, before, layout); err != nil {
		return zero, err
	}
	// No reply can replace the retained owner or turn observations into current
	// authority. Repeat every authority/owner/history join after helper await.
	check := func() error {
		current, e := authority.Policy()
		if e != nil || current != policy || ctx.Err() != nil {
			return ErrObservation
		}
		after, e := read()
		if e != nil || !reflect.DeepEqual(before, after) {
			return ErrObservation
		}
		return held.recheck()
	}
	if err = check(); err != nil {
		return zero, err
	}
	cp := *before.Workspace.Checkpoint
	cp.Members = append([]model.WorkspaceCommit(nil), cp.Members...)
	return &Observation{result: Result{Layout: layout, RootPath: before.Path, Checkpoint: cp}, check: check, close: held.close, cancel: cancel}, nil
}

func ready(policy payloadpolicy.Policy, r Request, b model.Bench) error {
	w := b.Workspace
	if b.Key != r.Key || w == nil || w.ID != r.BenchID || w.Phase != "ready" || w.Pending != nil || w.Stage != nil || w.Archive != nil || w.Transfer != nil || w.InitializationRepair != nil || w.Checkpoint == nil || b.Lease == nil || b.Lease.Holder != r.Holder || b.Lease.StopRequested || b.Path != filepath.Join(policy.DataRoot, "benches", r.Key) || b.ArchivedAt != "" || b.ReapedAt != "" {
		return ErrObservation
	}
	if e := b.Lease.Execution; e != nil {
		if e.Phase != "stopped" || e.Root != policy.CgroupRoot {
			return ErrObservation
		}
		if err := (execution.Host{}).Empty(e); err != nil {
			return errors.Join(ErrObservation, err)
		}
	}
	// Absence of a recorded execution is not a stopped/fenced assertion. This
	// endpoint reports only registration/checkpoint metadata under exclusion.
	return nil
}

func joinLayout(policy payloadpolicy.Policy, r Request, b model.Bench, l payloadexec.Layout) error {
	if l.Validate(policy) != nil || l.Scalar || l.Key != r.Key || l.ID != r.BenchID || b.Workspace == nil || b.Workspace.Checkpoint == nil {
		return ErrObservation
	}
	w, cp := b.Workspace, b.Workspace.Checkpoint
	if len(l.Members) != len(w.Members) || len(l.Members) != len(cp.Members) || !generation.MatchString(cp.GenerationID) || !commit(cp.Seal) || !digest.MatchString(cp.Digest) {
		return ErrObservation
	}
	for i, m := range l.Members {
		if m.Name != w.Members[i].Name || m.Name != cp.Members[i].Name || !commit(cp.Members[i].Commit) || w.Members[i].Path != filepath.Join(b.Path, m.Name) {
			return ErrObservation
		}
	}
	return nil
}
