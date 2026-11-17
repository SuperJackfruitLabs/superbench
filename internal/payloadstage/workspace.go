// Package payloadstage binds installed service authority and a current aggregate
// registration to the execution runner. It cannot create or convert a layout.
package payloadstage

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

var ErrRegistration = errors.New("registered private payload execution required")
var ErrCache = errors.New("registered payload cache transport is not available")

// Workspace snapshots the current core identity selected under the caller's
// ownership gate. Authority is independently installed/actual-process bound;
// fields in a Bench can never select another policy, helper or payload UID.
func Workspace(authority serviceidentity.Authority, bench *model.Bench, member string, root bool) (stageexec.PrepareTransport, error) {
	return workspaceTransport(authority, bench, member, root, nil, false)
}

// workspaceTransport snapshots the selected registration. A finite transport
// carries copied opaque stdin for the helper; it selects no other authority.
func workspaceTransport(authority serviceidentity.Authority, bench *model.Bench, member string, root bool, input []byte, finite bool) (stageexec.PrepareTransport, error) {
	if bench == nil || bench.Lease == nil || bench.Lease.Execution == nil || bench.Lease.Execution.Phase != "starting" || bench.Lease.StopRequested || bench.Lease.Continuation == "" || bench.Workspace == nil || bench.Workspace.ID == "" || bench.Workspace.Transfer != nil || bench.Workspace.Archive != nil || root && member != "" || !root && member == "" {
		return nil, ErrRegistration
	}
	copy := *bench
	group := *bench.Workspace
	group.Members = append([]model.WorkspaceMember(nil), group.Members...)
	copy.Workspace = &group
	lease := *bench.Lease
	scope := *bench.Lease.Execution
	lease.Execution = &scope
	copy.Lease = &lease
	if bench.Workspace.Stage != nil {
		stage := *bench.Workspace.Stage
		group.Stage = &stage
		if root || stage.Name != member {
			return nil, ErrRegistration
		}
	}
	if _, err := authority.Policy(); err != nil {
		return nil, err
	}
	return workspace(authority, &copy, member, root, input, finite), nil
}

// Scalar requires the fresh private layout marker. An installed service must
// never reinterpret a legacy same-UID checkout as a registered payload tree.
func Scalar(authority serviceidentity.Authority, bench *model.Bench) (stageexec.PrepareTransport, error) {
	if bench == nil || bench.Workspace != nil || bench.PayloadLayoutVersion == "" || model.ValidateScalarLayout(bench) != nil || bench.Lease == nil || bench.Lease.Execution == nil || bench.Lease.Execution.Phase != "starting" || bench.Lease.StopRequested || bench.ArchivedAt != "" {
		return nil, ErrRegistration
	}
	copy := *bench
	lease := *bench.Lease
	scope := *lease.Execution
	lease.Execution = &scope
	copy.Lease = &lease
	if _, err := authority.Policy(); err != nil {
		return nil, err
	}
	return workspace(authority, &copy, "", false, nil, false), nil
}
