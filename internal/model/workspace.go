package model

// Workspace is the aggregate identity held by one Bench lease and allocation.
// The scalar Repo/Branch/Mirror/Checkpoint fields are empty for this record kind.
// Readiness is a separate lifecycle transition; staged checkouts retain their
// initialization owner until preparation and complete checkpoint publication.
const WorkspaceRetainedRuns = 64

type Workspace struct {
	ID                   string
	CreationID           string
	CheckpointRepository string
	RecoveredFrom        *WorkspaceRecovery `json:",omitempty"`
	Phase                string
	ExplicitQuotaBytes   uint64 `json:",omitempty"`
	Members              []WorkspaceMember
	Preparation          []WorkspacePreparation         `json:",omitempty"`
	Stage                *WorkspaceStage                `json:",omitempty"`
	Pending              *WorkspaceCheckpoint           `json:",omitempty"`
	Checkpoint           *WorkspaceCheckpoint           `json:",omitempty"`
	Archive              *WorkspaceArchive              `json:",omitempty"`
	Unmerged             []WorkspaceMergeEvidence       `json:",omitempty"`
	InitializationRepair *WorkspaceInitializationRepair `json:",omitempty"`
	Transfer             *WorkspaceTransfer             `json:",omitempty"`
	TransferredFrom      *WorkspaceTransferOrigin       `json:",omitempty"`
}

// WorkspaceTransfer permanently relinquishes source task-ref writer authority.
// Initialization readiness remains independent from transfer availability.
type WorkspaceTransfer struct {
	ID, Digest, TargetKey, TargetID, CreationID, Phase string
	CrossHost                                          *WorkspaceHandoff          `json:",omitempty"`
	Recovery                                           *WorkspaceTransferRecovery `json:",omitempty"`
}

// WorkspaceTransferRecovery retains retired source authority as evidence only.
// It neither releases history nor asserts that lost task work was checkpointed.
type WorkspaceTransferRecovery struct {
	Mode, SourceRowDigest, RetiredAt, Holder string
	Execution                                *Execution      `json:",omitempty"`
	Stage                                    *WorkspaceStage `json:",omitempty"`
	Runs                                     []WorkspaceRetiredRun
}
type WorkspaceRetiredRun struct{ ID, Digest string }

// WorkspaceHandoff binds a transfer to the remote machine/state incarnation.
// It is evidence, never an attestation or writer capability.
type WorkspaceHandoff struct{ HostID, StateID, ArtifactDigest string }
type WorkspaceTransferOrigin struct {
	ID, Digest, SourceKey, SourceID string
	CrossHost                       *WorkspaceHandoff `json:",omitempty"`
}

// WorkspaceInitializationRepair preserves explicit initialization handoff and
// unreleased history authority across reviewed repair claims.
type WorkspaceInitializationRepair struct {
	ID, Digest, PreviousID string
	OperationDigest        string `json:",omitempty"`
	RetiredRuns            []string
	UncertainOnce          []string
}

type WorkspaceMember struct {
	Name                string
	Repository          string
	Branch              string
	Base                string
	Path                string
	Mirror              string
	Source              string
	ObjectFormat        string
	RequestedQuotaBytes uint64 `json:",omitempty"`
}

// WorkspaceCheckpoint binds a whole member vector to one immutable generation.
// Pending records have no Seal; completed records retain the confirmed seal.
type WorkspaceCheckpoint struct {
	GenerationID, Previous, Seal, Digest string
	Members                              []WorkspaceCommit
}
type WorkspaceCommit struct{ Name, Commit string }

type WorkspacePreparation struct{ Name, Digest, RunID string }

type WorkspaceStage struct{ Name, Digest string }

// WorkspaceRecovery records the exact remote seal used to select this new
// workspace. It transfers source commits, never leases or host allocations.
type WorkspaceRecovery struct{ Repository, BenchID, Commit, ManifestSHA256 string }

// WorkspaceArchive pins aggregate retention and deletion intent independently
// from the mutable filesystem. Merge is a whole-vector automatic observation.
type WorkspaceArchive struct {
	ArchivedAt, ReapAfter, FreezeDigest, FrozenAt, ReapingDigest, ReapedAt string
	Merge                                                                  []WorkspaceMergeEvidence `json:",omitempty"`
}
type WorkspaceMergeEvidence struct {
	Name     string
	Evidence ArchiveMergeEvidence
}
