// Package workspacebootstrap joins durable grouped creation to the fixed fresh
// payload helper. It grants no ownership and never adopts an existing allocation.
package workspacebootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacecopy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

var ErrBootstrap = errors.New("fresh workspace bootstrap refused")

// Specification copies the complete already-resolved durable source vector.
// Validation alone grants no authority; Stage additionally requires the actual
// installed service identity and original controlled create/recover operation.
func Specification(data string, b *model.Bench) (payloadbootstrap.Spec, error) {
	var zero payloadbootstrap.Spec
	if !filepath.IsAbs(data) || filepath.Clean(data) != data || workspacepolicy.Validate(b) != nil || b.Path != filepath.Join(data, "benches", b.Key) || b.Workspace.Phase != "materializing" || b.ProjectID != 0 || b.Lease == nil || b.Lease.StopRequested || b.Lease.Execution != nil || b.Lease.Continuation == "" || b.Workspace.InitializationRepair != nil || b.RegistrationID != "" || b.PayloadLayoutVersion != "" || b.AllocationRoot != "" {
		return zero, ErrBootstrap
	}
	w := b.Workspace
	s := payloadbootstrap.Spec{Version: payloadbootstrap.Version, Key: b.Key, ID: w.ID, CheckpointRepository: w.CheckpointRepository, QuotaBytes: b.RequestedQuotaBytes}
	for _, m := range w.Members {
		s.Members = append(s.Members, payloadbootstrap.Member{Name: m.Name, Repository: m.Repository, Branch: m.Branch, Source: m.Source, ObjectFormat: m.ObjectFormat})
	}
	if s.Validate() != nil {
		return zero, ErrBootstrap
	}
	return s, nil
}

// New supplies the staging callback; the workspace manager must retain its real
// host lock and durably bind the exact original owner/vector before invoking it.
func New(state, data string) func(context.Context, *model.Bench) error {
	return func(ctx context.Context, b *model.Bench) error { return Stage(ctx, state, data, b) }
}

func Stage(ctx context.Context, state, data string, b *model.Bench) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	spec, err := Specification(data, b)
	if err != nil {
		return err
	}
	original := workspacecopy.Copy(b)
	i, ok := operationcontrol.FromContext(ctx)
	if !ok || i.Key != b.Key || (i.Operation != "create" && i.Operation != "recover") {
		return operationcontrol.ErrAuthority
	}
	control := operationcontrol.ControlFromContext(ctx)
	if !control.Managed() || control.State != state {
		return operationcontrol.ErrAuthority
	}
	if err = operationcontrol.Check(ctx); err != nil {
		return err
	}
	authority, err := serviceidentity.ResolveService()
	if err != nil {
		return errors.Join(ErrBootstrap, err)
	}
	p, err := authority.Policy()
	if err != nil || p.StateRoot != state || p.DataRoot != data || control.Root != p.CgroupRoot {
		return ErrBootstrap
	}
	config, err := (hoststorage.Worker{State: state, Data: data}).Config()
	if err != nil || config == nil || config.PolicyOverride != "" {
		return ErrBootstrap
	}
	input, err := spec.Bytes()
	if err != nil {
		return err
	}
	raw, err := quotahelper.Bootstrap(ctx, quotahelper.Config{State: state, Data: data, Command: config.QuotaHelper}, spec.Key, spec.QuotaBytes, input)
	if err != nil {
		return err
	}
	if err = operationcontrol.Check(ctx); err != nil {
		return err
	}
	if err = authority.Recheck(); err != nil {
		return err
	}
	result, err := payloadbootstrap.ParseResult(raw, p, spec)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(b, original) {
		return ErrBootstrap
	}
	candidate := workspacecopy.Copy(original)
	candidate.ProjectID = result.Allocation.ProjectID
	candidate.QuotaBytes = result.Allocation.Bytes
	candidate.QuotaInodes = result.Allocation.Inodes
	if err = workspacepolicy.Validate(candidate); err != nil {
		return errors.Join(ErrBootstrap, err)
	}
	b.ProjectID, b.QuotaBytes, b.QuotaInodes = candidate.ProjectID, candidate.QuotaBytes, candidate.QuotaInodes
	return nil
}
