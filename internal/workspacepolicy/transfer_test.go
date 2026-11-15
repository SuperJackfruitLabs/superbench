package workspacepolicy

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func transferRecord(t *testing.T) *model.Bench {
	t.Helper()
	b := prepared()
	b.Workspace.Checkpoint = checkpoint(t, b)
	b.Workspace.Checkpoint.Seal = strings.Repeat("f", 40)
	b.Workspace.Phase = "ready"
	b.Lease = nil
	b.Workspace.Transfer = &model.WorkspaceTransfer{ID: "transfer1", Digest: strings.Repeat("1", 64), TargetKey: "target", TargetID: strings.Repeat("2", 32), CreationID: "creation1", Phase: "relinquishing"}
	return b
}
func TestTransferAuthorityRequiresCompleteUnleasedReadySource(t *testing.T) {
	if err := Validate(transferRecord(t)); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*model.Bench){
		"held":          func(b *model.Bench) { b.Lease = &model.Lease{Token: strings.Repeat("a", 48), Holder: "worker"} },
		"no-checkpoint": func(b *model.Bench) { b.Workspace.Checkpoint = nil },
		"pending":       func(b *model.Bench) { b.Workspace.Pending = checkpoint(t, b) },
		"same-key":      func(b *model.Bench) { b.Workspace.Transfer.TargetKey = b.Key },
		"same-id":       func(b *model.Bench) { b.Workspace.Transfer.TargetID = b.Workspace.ID },
		"bad-digest":    func(b *model.Bench) { b.Workspace.Transfer.Digest = strings.Repeat("F", 64) },
		"bad-phase":     func(b *model.Bench) { b.Workspace.Transfer.Phase = "ready" },
		"bad-request":   func(b *model.Bench) { b.Workspace.Transfer.ID = "../transfer" },
		"bad-creation":  func(b *model.Bench) { b.Workspace.Transfer.CreationID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			b := transferRecord(t)
			change(b)
			if Validate(b) == nil {
				t.Fatal("invalid transfer authority accepted")
			}
		})
	}
}
func TestIncomingOriginIsBoundedAndHistoricalSchemaCannotAdoptIt(t *testing.T) {
	b := validRecord()
	b.Workspace.TransferredFrom = &model.WorkspaceTransferOrigin{ID: "transfer1", Digest: strings.Repeat("1", 64), SourceKey: "source", SourceID: strings.Repeat("2", 32)}
	if err := Validate(b); err != nil {
		t.Fatal(err)
	}
	if ValidateSchema20(b) == nil {
		t.Fatal("schema20 adopted transfer")
	}
	for name, change := range map[string]func(*model.WorkspaceTransferOrigin){"same-key": func(o *model.WorkspaceTransferOrigin) { o.SourceKey = b.Key }, "same-id": func(o *model.WorkspaceTransferOrigin) { o.SourceID = b.Workspace.ID }, "upper-digest": func(o *model.WorkspaceTransferOrigin) { o.Digest = strings.Repeat("A", 64) }, "long-id": func(o *model.WorkspaceTransferOrigin) { o.ID = strings.Repeat("x", 81) }} {
		t.Run(name, func(t *testing.T) {
			c := *b
			w := *b.Workspace
			o := *w.TransferredFrom
			c.Workspace = &w
			w.TransferredFrom = &o
			change(&o)
			if Validate(&c) == nil {
				t.Fatal("invalid origin accepted")
			}
		})
	}
}
