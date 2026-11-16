package payloadstage

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"testing"
)

func TestWorkspaceCannotTreatMissingInstalledAuthorityAsOrdinaryFallback(t *testing.T) {
	b := &model.Bench{Key: "card", Workspace: &model.Workspace{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Lease: &model.Lease{Continuation: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Execution: &model.Execution{Phase: "starting"}}}
	prepare, err := Workspace(serviceidentity.Authority{}, b, "a", false)
	if prepare != nil || !errors.Is(err, serviceidentity.ErrAuthority) {
		t.Fatalf("zero authority gained transport: %v %v", prepare, err)
	}
}
func TestMissingCoreExecutionCannotAdoptExistingFilesystemLayout(t *testing.T) {
	for _, b := range []*model.Bench{nil, {Key: "card"}, {Key: "card", Workspace: &model.Workspace{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}, {Key: "card", Workspace: &model.Workspace{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, Lease: &model.Lease{Execution: &model.Execution{Phase: "stopped"}}}} {
		prepare, err := Workspace(serviceidentity.Authority{}, b, "a", false)
		if prepare != nil || !errors.Is(err, ErrRegistration) {
			t.Fatalf("missing/stopped core scope gained layout authority: %v %v", prepare, err)
		}
	}
}

func TestScalarLegacyLayoutRefusesBeforeInstalledAuthority(t *testing.T) {
	b := &model.Bench{Key: "card", RegistrationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Path: "/data/benches/card", Lease: &model.Lease{Execution: &model.Execution{Phase: "starting"}}}
	prepare, err := Scalar(serviceidentity.Authority{}, b)
	if prepare != nil || !errors.Is(err, ErrRegistration) {
		t.Fatalf("legacy layout gained transport: %v %v", prepare, err)
	}
	b.PayloadLayoutVersion = "superbench.payload-layout.v1"
	b.AllocationRoot = b.Path
	b.Path += "/worktree"
	prepare, err = Scalar(serviceidentity.Authority{}, b)
	if prepare != nil || !errors.Is(err, serviceidentity.ErrAuthority) {
		t.Fatalf("uninstalled scalar transport: %v %v", prepare, err)
	}
	b.Lease.StopRequested = true
	prepare, err = Scalar(serviceidentity.Authority{}, b)
	if prepare != nil || !errors.Is(err, ErrRegistration) {
		t.Fatalf("stopped scalar gained transport: %v %v", prepare, err)
	}
}
