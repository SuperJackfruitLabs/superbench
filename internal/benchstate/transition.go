package benchstate

import (
	"errors"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

type identityRecord struct {
	workspace                  *model.Workspace
	path                       string
	requested                  uint64
	project                    uint32
	bytes, inodes              uint64
	token, continuation, phase string
}

// IdentitySnapshot owns copied immutable metadata, independent of callback maps.
type IdentitySnapshot map[string]identityRecord

func Capture(records Database) IdentitySnapshot {
	snapshot := IdentitySnapshot{}
	for key, b := range records {
		entry := identityRecord{}
		if b != nil && b.Workspace != nil {
			w := *b.Workspace
			w.Members = append([]model.WorkspaceMember(nil), w.Members...)
			entry.phase = w.Phase
			w.Phase = ""
			entry.workspace = &w
			entry.path = b.Path
			entry.requested = b.RequestedQuotaBytes
			entry.project = b.ProjectID
			entry.bytes = b.QuotaBytes
			entry.inodes = b.QuotaInodes
			if b.Lease != nil {
				entry.token = b.Lease.Token
				entry.continuation = b.Lease.Continuation
			}
		}
		snapshot[key] = entry
	}
	return snapshot
}

func ValidateTransition(before IdentitySnapshot, after Database) error {
	invalid := errors.New("immutable workspace identity or ownership changed")
	for key, old := range before {
		b, exists := after[key]
		if old.workspace == nil {
			if exists && b != nil && b.Workspace != nil {
				return invalid
			}
			continue
		}
		if !exists || b == nil || b.Workspace == nil || b.Lease == nil {
			return invalid
		}
		w := *b.Workspace
		phase := w.Phase
		w.Phase = ""
		if !reflect.DeepEqual(old.workspace, &w) || old.path != b.Path || old.requested != b.RequestedQuotaBytes || old.token != b.Lease.Token || old.continuation != b.Lease.Continuation {
			return invalid
		}
		if phase != old.phase && !(old.phase == "materializing" && phase == "initializing") {
			return invalid
		}
		if old.project != 0 && (old.project != b.ProjectID || old.bytes != b.QuotaBytes || old.inodes != b.QuotaInodes) {
			return invalid
		}
	}
	return nil
}
