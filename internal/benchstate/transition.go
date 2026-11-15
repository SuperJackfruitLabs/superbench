package benchstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

type identityRecord struct {
	rowDigest           string
	workspace           *model.Workspace
	path                string
	requested           uint64
	project             uint32
	bytes, inodes       uint64
	phase               string
	lease               *model.Lease
	pending, checkpoint *model.WorkspaceCheckpoint
	preparation         []model.WorkspacePreparation
	stage               *model.WorkspaceStage
	archive             *model.WorkspaceArchive
	unmerged            []model.WorkspaceMergeEvidence
	repair              *model.WorkspaceInitializationRepair
	transfer            *model.WorkspaceTransfer
}

// IdentitySnapshot owns copied immutable metadata and lifecycle evidence.
type IdentitySnapshot map[string]identityRecord

func copyCheckpoint(c *model.WorkspaceCheckpoint) *model.WorkspaceCheckpoint {
	if c == nil {
		return nil
	}
	v := *c
	v.Members = append([]model.WorkspaceCommit(nil), c.Members...)
	return &v
}
func copyLease(l *model.Lease) *model.Lease {
	if l == nil {
		return nil
	}
	v := *l
	v.RetainedRuns = append([]string(nil), l.RetainedRuns...)
	if l.Execution != nil {
		e := *l.Execution
		v.Execution = &e
	}
	return &v
}
func Capture(records Database) IdentitySnapshot {
	out := IdentitySnapshot{}
	for key, b := range records {
		entry := identityRecord{}
		if b != nil && b.Workspace != nil {
			raw, _ := json.Marshal(b)
			sum := sha256.Sum256(raw)
			entry.rowDigest = hex.EncodeToString(sum[:])
			w := *b.Workspace
			if w.RecoveredFrom != nil {
				source := *w.RecoveredFrom
				w.RecoveredFrom = &source
			}
			w.Members = append([]model.WorkspaceMember(nil), w.Members...)
			if w.TransferredFrom != nil {
				origin := *w.TransferredFrom
				if origin.CrossHost != nil {
					h := *origin.CrossHost
					origin.CrossHost = &h
				}
				w.TransferredFrom = &origin
			}
			entry.transfer = copyTransfer(w.Transfer)
			w.Transfer = nil
			entry.repair = copyInitializationRepair(w.InitializationRepair)
			w.InitializationRepair = nil
			if w.Stage != nil {
				stage := *w.Stage
				entry.stage = &stage
			}
			w.Stage = nil
			entry.preparation = append([]model.WorkspacePreparation(nil), w.Preparation...)
			w.Preparation = nil
			entry.archive = copyArchive(w.Archive)
			entry.unmerged = append([]model.WorkspaceMergeEvidence(nil), w.Unmerged...)
			w.Archive = nil
			w.Unmerged = nil
			entry.phase = w.Phase
			entry.pending = copyCheckpoint(w.Pending)
			entry.checkpoint = copyCheckpoint(w.Checkpoint)
			w.Phase = ""
			w.Pending = nil
			w.Checkpoint = nil
			entry.workspace = &w
			entry.path = b.Path
			entry.requested = b.RequestedQuotaBytes
			entry.project = b.ProjectID
			entry.bytes = b.QuotaBytes
			entry.inodes = b.QuotaInodes
			entry.lease = copyLease(b.Lease)
		}
		out[key] = entry
	}
	return out
}
func sameVector(a, b *model.WorkspaceCheckpoint) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := *a, *b
	x.Seal = ""
	y.Seal = ""
	return reflect.DeepEqual(x, y)
}
func checkpointTransition(old identityRecord, w *model.Workspace) bool {
	changed := !reflect.DeepEqual(old.checkpoint, w.Checkpoint)
	if changed {
		if old.pending == nil || w.Pending != nil || w.Checkpoint == nil || w.Checkpoint.Seal == "" || !sameVector(old.pending, w.Checkpoint) {
			return false
		}
	} else if old.pending != nil && !reflect.DeepEqual(old.pending, w.Pending) {
		return false
	}
	if old.pending == nil && w.Pending != nil {
		if changed {
			return false
		}
		previous := ""
		if old.checkpoint != nil {
			previous = old.checkpoint.Seal
		}
		if w.Pending.Previous != previous {
			return false
		}
	}
	return true
}
func phaseTransition(old string, b *model.Bench) bool {
	next := b.Workspace.Phase
	if next == old {
		return true
	}
	switch old {
	case "materializing":
		return next == "initializing"
	case "initializing":
		return next == "prepared"
	case "prepared":
		return next == "initializing" || next == "ready" && b.Workspace.Checkpoint != nil && b.Workspace.Pending == nil && b.Lease == nil
	case "ready":
		return next == "archived" && b.Workspace.Archive != nil && b.Lease == nil
	case "archived":
		return next == "reaping"
	case "reaping":
		return next == "reaped"
	}
	return false
}
func executionTransition(old, next *model.Execution, retained []string) bool {
	if old == nil {
		return next == nil || next.Phase == "starting"
	}
	if next == nil || next.ID != old.ID {
		if old.Phase != "stopped" {
			return false
		}
		found := false
		for _, id := range retained {
			found = found || id == old.ID
		}
		return found && (next == nil || next.Phase == "starting")
	}
	a, b := *old, *next
	a.Phase = ""
	b.Phase = ""
	a.PID = 0
	b.PID = 0
	if !reflect.DeepEqual(a, b) {
		return false
	}
	if old.PID != next.PID && (old.Phase != "starting" || old.PID != 0 || next.PID <= 0) {
		return false
	}
	switch old.Phase {
	case "starting":
		return next.Phase == "starting" || next.Phase == "running" || next.Phase == "stopped"
	case "running":
		return next.Phase == "running" || next.Phase == "stopped"
	case "stopped":
		return next.Phase == "stopped"
	}
	return false
}
func leaseTransition(old *model.Lease, b *model.Bench) bool {
	next := b.Lease
	if old == nil {
		return next == nil || b.Workspace.Phase == "ready" && next.Execution == nil && len(next.RetainedRuns) == 0 && !next.StopRequested && next.Continuation != "" && b.Workspace.Stage == nil && b.Workspace.Pending == nil && len(b.Workspace.Preparation) == 0
	}
	if next == nil {
		return b.Workspace.Phase == "ready" && b.Workspace.Checkpoint != nil && b.Workspace.Pending == nil && (old.Execution == nil || old.Execution.Phase == "stopped")
	}
	if old.Token != next.Token || old.Holder != next.Holder || old.StopRequested && !next.StopRequested {
		return false
	}
	if old.Continuation != next.Continuation && !(next.StopRequested && next.Continuation == "") {
		return false
	}
	if len(next.RetainedRuns) < len(old.RetainedRuns) || !slices.Equal(old.RetainedRuns, next.RetainedRuns[:len(old.RetainedRuns)]) {
		return false
	}
	return executionTransition(old.Execution, next.Execution, next.RetainedRuns)
}

func preparationTransition(old identityRecord, b *model.Bench) bool {
	if slices.Equal(old.preparation, b.Workspace.Preparation) {
		return true
	}
	if old.pending != nil || b.Workspace.Pending != nil {
		return false
	}
	if old.lease == nil {
		return old.phase == "ready" && b.Workspace.Phase == "ready" && b.Lease != nil && len(b.Workspace.Preparation) == 0
	}
	if b.Lease == nil {
		return b.Workspace.Phase == "ready" && len(b.Workspace.Preparation) == 0 && (old.lease.Execution == nil || old.lease.Execution.Phase == "stopped")
	}
	if old.lease.Execution != nil && old.lease.Execution.Phase != "stopped" || b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
		return false
	}
	return old.phase != "materializing" && b.Workspace.Phase != "materializing"
}

func stageTransition(old identityRecord, b *model.Bench) bool {
	var previous, current *model.Execution
	if old.lease != nil {
		previous = old.lease.Execution
	}
	if b.Lease != nil {
		current = b.Lease.Execution
	}
	if previous != nil && current != nil && previous.ID == current.ID {
		return reflect.DeepEqual(old.stage, b.Workspace.Stage)
	}
	if old.stage != nil && (previous == nil || previous.Phase != "stopped") {
		return false
	}
	return true
}

func copyArchive(a *model.WorkspaceArchive) *model.WorkspaceArchive {
	if a == nil {
		return nil
	}
	c := *a
	c.Merge = append([]model.WorkspaceMergeEvidence(nil), a.Merge...)
	return &c
}
func unmergedTransition(old identityRecord, b *model.Bench) bool {
	if slices.Equal(old.unmerged, b.Workspace.Unmerged) {
		return true
	}
	return old.archive == nil && old.phase == "ready" && b.Workspace.Phase == "ready" && old.lease != nil && b.Lease != nil && old.pending != nil && b.Workspace.Pending == nil && !reflect.DeepEqual(old.checkpoint, b.Workspace.Checkpoint)
}
func archiveTransition(old identityRecord, b *model.Bench) bool {
	next := b.Workspace.Archive
	if old.archive == nil {
		if next == nil {
			return true
		}
		return old.phase == "ready" && b.Workspace.Phase == "archived" && old.lease == nil && b.Lease == nil && old.pending == nil && b.Workspace.Pending == nil && reflect.DeepEqual(old.checkpoint, b.Workspace.Checkpoint) && slices.Equal(old.preparation, b.Workspace.Preparation) && slices.Equal(old.unmerged, b.Workspace.Unmerged)
	}
	if next == nil || old.project != b.ProjectID || old.bytes != b.QuotaBytes || old.inodes != b.QuotaInodes || !reflect.DeepEqual(old.checkpoint, b.Workspace.Checkpoint) || !slices.Equal(old.preparation, b.Workspace.Preparation) || !slices.Equal(old.unmerged, b.Workspace.Unmerged) {
		return false
	}
	a, c := *old.archive, *next
	a.FrozenAt = ""
	c.FrozenAt = ""
	a.ReapingDigest = ""
	c.ReapingDigest = ""
	a.ReapedAt = ""
	c.ReapedAt = ""
	if !reflect.DeepEqual(a, c) {
		return false
	}
	if old.archive.FrozenAt != "" && old.archive.FrozenAt != next.FrozenAt || old.archive.ReapingDigest != "" && old.archive.ReapingDigest != next.ReapingDigest || old.archive.ReapedAt != "" && old.archive.ReapedAt != next.ReapedAt {
		return false
	}
	return true
}

func ValidateTransition(before IdentitySnapshot, after Database) error {
	invalid := errors.New("immutable workspace identity, ownership or lifecycle changed")
	for key, old := range before {
		b, exists := after[key]
		if old.workspace == nil {
			if exists && b != nil && b.Workspace != nil {
				return invalid
			}
			continue
		}
		if !exists || b == nil || b.Workspace == nil {
			return invalid
		}
		w := *b.Workspace
		w.Phase = ""
		w.Pending = nil
		w.Checkpoint = nil
		w.Preparation = nil
		w.Stage = nil
		w.Archive = nil
		w.Unmerged = nil
		w.InitializationRepair = nil
		w.Transfer = nil
		if !reflect.DeepEqual(old.workspace, &w) || old.path != b.Path || old.requested != b.RequestedQuotaBytes {
			return invalid
		}
		if old.project != 0 && (old.project != b.ProjectID || old.bytes != b.QuotaBytes || old.inodes != b.QuotaInodes) {
			return invalid
		}
		if !transferTransition(old, b, before, after) {
			return invalid
		}
		if !reflect.DeepEqual(old.repair, b.Workspace.InitializationRepair) {
			if !initializationRepairTransition(old, b) {
				return invalid
			}
			continue
		}
		if !archiveTransition(old, b) || !unmergedTransition(old, b) || !stageTransition(old, b) || !preparationTransition(old, b) || !checkpointTransition(old, b.Workspace) || !phaseTransition(old.phase, b) || !leaseTransition(old.lease, b) {
			return invalid
		}
	}
	return newTransferOrigins(before, after)
}
