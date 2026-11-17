// Package workspaceapi projects private aggregate operation results to closed wire DTOs.
package workspaceapi

import (
	"errors"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

func executionV2(e *bench.Execution) *protocolv2.Execution {
	if e == nil {
		return nil
	}
	return &protocolv2.Execution{ID: e.ID, Phase: e.Phase}
}

func leaseV2(l *bench.Lease) *protocolv2.LeaseSummary {
	if l == nil {
		return nil
	}
	mode := "cooperative"
	if l.Execution != nil {
		mode = "supervised"
	}
	return &protocolv2.LeaseSummary{Holder: l.Holder, Mode: mode, StopRequested: l.StopRequested, Execution: executionV2(l.Execution)}
}

func vectorV2(members []model.WorkspaceCommit) []protocolv2.Commit {
	out := make([]protocolv2.Commit, 0, len(members))
	for _, m := range members {
		out = append(out, protocolv2.Commit{Name: m.Name, Commit: m.Commit})
	}
	return out
}

func workspaceV2(b *bench.Bench) protocolv2.Workspace {
	w := b.Workspace
	v := protocolv2.Workspace{Key: b.Key, BenchID: w.ID, Phase: w.Phase,
		MemberCount: len(w.Members), CheckpointRepository: w.CheckpointRepository,
		QuotaBytes: b.QuotaBytes, QuotaInodes: b.QuotaInodes, Lease: leaseV2(b.Lease),
		ReadyForAcquisition: w.Phase == "ready" && b.Lease == nil && w.Pending == nil && w.Archive == nil && w.Transfer == nil}
	if w.Transfer != nil {
		v.TransferState = w.Transfer.Phase
	}
	if c := w.Checkpoint; c != nil {
		v.Checkpoint = &protocolv2.Checkpoint{GenerationID: c.GenerationID, ManifestSHA256: c.Digest,
			Members: vectorV2(c.Members), Seal: c.Seal, PreviousSeal: c.Previous}
	}
	if p := w.Pending; p != nil {
		v.Pending = &protocolv2.Pending{GenerationID: p.GenerationID, ManifestSHA256: p.Digest,
			Members: vectorV2(p.Members), PreviousSeal: p.Previous}
	}
	if r := w.RecoveredFrom; r != nil {
		v.RecoveredFrom = &protocolv2.Recovery{BenchID: r.BenchID, Seal: r.Commit, ManifestSHA256: r.ManifestSHA256}
	}
	if a := w.Archive; a != nil {
		v.Archive = &protocolv2.Archive{ArchivedAt: a.ArchivedAt, ReapAfter: a.ReapAfter,
			FreezeDigest: a.FreezeDigest, FrozenAt: a.FrozenAt, ReapingDigest: a.ReapingDigest,
			ReapedAt: a.ReapedAt, Automatic: len(a.Merge) != 0}
	}
	return v
}

func Inventory(b *bench.Bench) protocolv2.Inventory {
	if b.Workspace != nil {
		v := &protocolv2.WorkspaceInventory{Workspace: workspaceV2(b), Kind: "workspace", Members: make([]protocolv2.Member, 0, len(b.Workspace.Members))}
		for _, m := range b.Workspace.Members {
			v.Members = append(v.Members, protocolv2.Member{Name: m.Name, Branch: m.Branch,
				Base: m.Base, ObjectFormat: m.ObjectFormat, SourceCommit: m.Source})
		}
		return v
	}
	v := &protocolv2.ScalarInventory{Kind: "scalar", Key: b.Key, Branch: b.Branch, State: "active",
		QuotaBytes: b.QuotaBytes, QuotaInodes: b.QuotaInodes, Checkpoint: b.Checkpoint, Lease: leaseV2(b.Lease)}
	if b.ArchivedAt != "" {
		v.State = "archived"
		if b.ReapingDigest != "" {
			v.State = "reaping"
		}
		if b.ReapedAt != "" {
			v.State = "reaped"
		}
		v.Archive = &protocolv2.ScalarArchive{ArchivedAt: b.ArchivedAt, ReapAfter: b.ReapAfter,
			FreezeDigest: b.ArchiveFreezeDigest, FrozenAt: b.ArchiveFrozenAt, ReapingDigest: b.ReapingDigest,
			ReapedAt: b.ReapedAt, Automatic: b.ArchiveMerge != nil}
	}
	return v
}

// The operations layer authorizes capabilities independently of this projection.
// Private records can never be marshaled through the closed wire result types.
func Operation(r protocolv2.Request, out *workspaceops.Result, operationErr error) (protocolv2.Result, protocolv2.Authority, error) {
	proof := protocolv2.Authority{}
	if out == nil || out.Workspace == nil {
		return nil, proof, nil
	}
	b := out.Workspace
	if b.Workspace == nil {
		return nil, proof, errors.New("aggregate result required")
	}
	if (r.Operation == "create" || r.Operation == "recover") && !out.RootPath {
		return nil, proof, workspaceops.ErrConflict
	}
	m := protocolv2.Mutation{Workspace: workspaceV2(b)}
	if out.GrantToken != "" {
		if b.Lease == nil {
			return nil, proof, errors.New("grant has no retained owner")
		}
		m.Grant = &protocolv2.Grant{LeaseToken: out.GrantToken}
		proof.GrantToken = out.GrantToken
		proof.Fenced = r.Operation == "stop_admin" && operationErr == nil
	}
	if out.RootPath && (r.Operation == "create" || r.Operation == "recover" || m.Grant != nil) {
		if !utf8.ValidString(b.Path) || len(b.Path) > 4096 {
			return nil, proof, errors.New("workspace root exceeds response bound")
		}
		m.RootPath, proof.RootPath = b.Path, b.Path
	}
	if r.Operation != "run" && r.Operation != "setup" {
		return &m, proof, nil
	}
	run := protocolv2.RunOutcome{Mutation: m, Execution: executionV2(out.Execution),
		ExitCode: out.ExitCode, LogTruncated: out.LogTruncated, IO: out.IO}
	// A signal or unknown outcome has no numeric command exit status on the wire.
	if run.ExitCode != nil && (*run.ExitCode < 0 || *run.ExitCode > 255) {
		run.ExitCode = nil
	}
	if r.Operation == "run" {
		return &run, proof, nil
	}
	setup := &protocolv2.SetupOutcome{RunOutcome: run, Preparation: make([]protocolv2.Preparation, 0, len(out.Preparation))}
	for _, p := range out.Preparation {
		setup.Preparation = append(setup.Preparation, protocolv2.Preparation{Name: p.Name, Status: p.Status})
	}
	return setup, proof, nil
}
