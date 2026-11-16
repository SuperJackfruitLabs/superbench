package main

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"

// scalarRecord is the existing local CLI presentation, independent of the
// private durable row. New storage authority must not widen this JSON shape.
type scalarRecord struct {
	Key, Repo, Branch, Path, Mirror, Checkpoint string
	CreatedFrom                                 string `json:",omitempty"`
	ArchivedAt                                  string `json:",omitempty"`
	ReapAfter                                   string `json:",omitempty"`
	ArchiveFreezeDigest                         string `json:",omitempty"`
	ArchiveFrozenAt                             string `json:",omitempty"`
	ArchiveMerge                                any    `json:",omitempty"`
	ArchiveUnmerged                             any    `json:",omitempty"`
	ReapingDigest                               string `json:",omitempty"`
	ReapedAt                                    string `json:",omitempty"`
	ProjectID                                   uint32
	QuotaBytes                                  uint64
	RequestedQuotaBytes                         uint64 `json:",omitempty"`
	QuotaInodes                                 uint64
	Lease                                       *bench.Lease `json:",omitempty"`
	Workspace                                   any          `json:",omitempty"`
}

func scalarView(b *bench.Bench) *scalarRecord {
	if b == nil {
		return nil
	}
	v := &scalarRecord{Key: b.Key, Repo: b.Repo, Branch: b.Branch, Path: b.Path, Mirror: b.Mirror, Checkpoint: b.Checkpoint, CreatedFrom: b.CreatedFrom, ArchivedAt: b.ArchivedAt, ReapAfter: b.ReapAfter, ArchiveFreezeDigest: b.ArchiveFreezeDigest, ArchiveFrozenAt: b.ArchiveFrozenAt, ReapingDigest: b.ReapingDigest, ReapedAt: b.ReapedAt, ProjectID: b.ProjectID, QuotaBytes: b.QuotaBytes, RequestedQuotaBytes: b.RequestedQuotaBytes, QuotaInodes: b.QuotaInodes, Lease: b.Lease}
	if b.ArchiveMerge != nil {
		v.ArchiveMerge = b.ArchiveMerge
	}
	if b.ArchiveUnmerged != nil {
		v.ArchiveUnmerged = b.ArchiveUnmerged
	}
	if b.Workspace != nil {
		v.Workspace = b.Workspace
	}
	return v
}
func scalarOutput(value any) any {
	switch v := value.(type) {
	case *bench.Bench:
		return scalarView(v)
	case bench.Bench:
		return scalarView(&v)
	case []bench.Bench:
		if v == nil {
			return ([]*scalarRecord)(nil)
		}
		out := make([]*scalarRecord, 0, len(v))
		for i := range v {
			out = append(out, scalarView(&v[i]))
		}
		return out
	case *bench.RunResult:
		if v == nil {
			return nil
		}
		return struct {
			Execution    *bench.Execution
			Bench        *scalarRecord
			ExitCode     int
			LogTruncated bool
		}{v.Execution, scalarView(v.Bench), v.ExitCode, v.LogTruncated}
	default:
		return value
	}
}
