// Package workspacebootstrap joins durable grouped creation to the fixed fresh
// payload helper. It grants no ownership and never adopts an existing allocation.
package workspacebootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	if w.Generation != 0 {
		s.Version, s.Generation, s.Previous = payloadbootstrap.GenerationVersion, w.Generation, w.Previous
	}
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
	if err = MoveLayoutAside(state, b); err != nil {
		return err
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

// MaxRegistration bounds the layout registration read, as the tree route does.
const MaxRegistration = 256 << 10

// MoveLayoutAside renames the layout registration of a later generation's
// reclaimed predecessor, which still holds the key's name in the daemon's
// registry, to KEY.PREVIOUS.reclaimed, where it stays as evidence and no
// route reads it. The fixed helper publishes the new generation's under the
// key exclusively, and refuses to begin while the old one is still there. A
// first generation, an absent registration (already moved) and this
// generation's own are left alone; one naming any other workspace refuses.
func MoveLayoutAside(state string, b *model.Bench) error {
	if b.Workspace.Generation == 0 {
		return nil
	}
	dir := filepath.Join(state, "payload-layouts")
	path, target := filepath.Join(dir, b.Key+".json"), filepath.Join(dir, b.Key+"."+b.Workspace.Previous+".reclaimed")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var named struct{ Key, ID string }
	var raw []byte
	if err == nil && info.Mode().IsRegular() && info.Size() <= MaxRegistration {
		raw, err = os.ReadFile(path)
	}
	if err == nil && json.Unmarshal(raw, &named) == nil && named.Key == b.Key && named.ID == b.Workspace.ID {
		return nil // this generation's own: the helper decides, as on any retry
	}
	if err != nil || json.Unmarshal(raw, &named) != nil || named.Key != b.Key || named.ID != b.Workspace.Previous {
		return errors.Join(ErrBootstrap, errors.New("the key's layout registration is not its reclaimed generation's"), err)
	}
	if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrBootstrap, errors.New("a moved-aside layout registration already exists"), err)
	}
	if err = os.Rename(path, target); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
