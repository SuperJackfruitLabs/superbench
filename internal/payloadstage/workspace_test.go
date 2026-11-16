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
