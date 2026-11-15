package benchstate

import (
	"errors"
	"reflect"
	"slices"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func validateTransferLinks(records Database) error {
	invalid := errors.New("workspace transfer source and destination links differ")
	targets, targetIDs, ids := map[string]bool{}, map[string]bool{}, map[string]bool{}
	identities := map[string]string{}
	for key, b := range records {
		if b != nil && b.Workspace != nil {
			identities[b.Workspace.ID] = key
		}
	}
	for _, b := range records {
		if b == nil || b.Workspace == nil {
			continue
		}
		w := b.Workspace
		if t := w.Transfer; t != nil {
			if targets[t.TargetKey] || targetIDs[t.TargetID] || ids[t.ID] {
				return invalid
			}
			if key, exists := identities[t.TargetID]; exists && key != t.TargetKey {
				return invalid
			}
			targets[t.TargetKey] = true
			targetIDs[t.TargetID] = true
			ids[t.ID] = true
			if target := records[t.TargetKey]; target != nil {
				if t.Phase != "transferred" || !linkedTransfer(b, target) {
					return invalid
				}
			}
		}
		if t := w.TransferredFrom; t != nil {
			if source := records[t.SourceKey]; source == nil || !linkedTransfer(source, b) {
				return invalid
			}
		}
	}
	return nil
}

func linkedTransfer(source, target *model.Bench) bool {
	if source == nil || target == nil || source.Workspace == nil || target.Workspace == nil {
		return false
	}
	a, b := source.Workspace.Transfer, target.Workspace.TransferredFrom
	return a != nil && b != nil && a.Phase == "transferred" && a.ID == b.ID && a.Digest == b.Digest && a.TargetKey == target.Key && a.TargetID == target.Workspace.ID && a.CreationID == target.Workspace.CreationID && b.SourceKey == source.Key && b.SourceID == source.Workspace.ID
}

func transferTransition(old identityRecord, b *model.Bench, before IdentitySnapshot, after Database) bool {
	next := b.Workspace.Transfer
	if old.transfer == nil && next == nil {
		return true
	}
	if next == nil || b.Lease != nil || old.pending != nil || b.Workspace.Pending != nil || b.Workspace.Stage != nil || !reflect.DeepEqual(old.checkpoint, b.Workspace.Checkpoint) || !slices.Equal(old.preparation, b.Workspace.Preparation) || !slices.Equal(old.unmerged, b.Workspace.Unmerged) || !reflect.DeepEqual(old.repair, b.Workspace.InitializationRepair) || old.project != b.ProjectID || old.bytes != b.QuotaBytes || old.inodes != b.QuotaInodes {
		return false
	}
	if old.transfer == nil {
		if !retirementTransition(old, next.Recovery) {
			return false
		}
		_, existed := before[next.TargetKey]
		return !existed && after[next.TargetKey] == nil && old.phase == "ready" && b.Workspace.Phase == "ready" && old.archive == nil && b.Workspace.Archive == nil && old.checkpoint != nil && next.Phase == "relinquishing"
	}
	a, c := *old.transfer, *next
	a.Phase = ""
	c.Phase = ""
	if !reflect.DeepEqual(a, c) {
		return false
	}
	if old.transfer.Phase == "relinquishing" {
		return next.Phase == "relinquishing" || next.Phase == "transferred" && old.phase == b.Workspace.Phase && reflect.DeepEqual(old.archive, b.Workspace.Archive)
	}
	return old.transfer.Phase == "transferred" && next.Phase == "transferred"
}

func newTransferOrigins(before IdentitySnapshot, after Database) error {
	for key, b := range after {
		if b != nil && b.Workspace != nil && b.Workspace.Transfer != nil && before[key].workspace == nil {
			return errors.New("outgoing transfer must begin on an existing ready source")
		}
		if b == nil || b.Workspace == nil || b.Workspace.TransferredFrom == nil || before[key].workspace != nil {
			continue
		}
		w, l := b.Workspace, b.Lease
		if w.Phase != "materializing" || l == nil || l.Holder != "initialization" || l.StopRequested || l.Continuation == "" || l.Execution != nil || len(l.RetainedRuns) != 0 || w.Pending != nil || w.Checkpoint != nil || len(w.Preparation) != 0 || w.Stage != nil || w.Archive != nil || w.InitializationRepair != nil || w.Transfer != nil {
			return errors.New("transferred destination must begin with fresh initialization authority")
		}
		source := after[w.TransferredFrom.SourceKey]
		if !linkedTransfer(source, b) || source.Workspace.Checkpoint == nil || len(w.Members) != len(source.Workspace.Members) || len(source.Workspace.Checkpoint.Members) != len(source.Workspace.Members) {
			return errors.New("transferred destination does not match sealed source inventory")
		}
		for i, m := range w.Members {
			s := source.Workspace.Members[i]
			c := source.Workspace.Checkpoint.Members[i]
			if m.Name != s.Name || m.Repository != s.Repository || m.Branch != s.Branch || m.ObjectFormat != s.ObjectFormat || c.Name != m.Name || m.Source != c.Commit {
				return errors.New("transferred destination changed original task refs or sealed commits")
			}
		}
	}
	return validateTransferLinks(after)
}
