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
	Phase                string
	ExplicitQuotaBytes   uint64 `json:",omitempty"`
	Members              []WorkspaceMember
	Preparation          []WorkspacePreparation `json:",omitempty"`
	Stage                *WorkspaceStage        `json:",omitempty"`
	Pending              *WorkspaceCheckpoint   `json:",omitempty"`
	Checkpoint           *WorkspaceCheckpoint   `json:",omitempty"`
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
