package model

// Workspace is the aggregate identity held by one Bench lease and allocation.
// The scalar Repo/Branch/Mirror/Checkpoint fields are empty for this record kind.
// Readiness is a separate lifecycle transition; staged checkouts retain their
// initialization owner until preparation and complete checkpoint publication.
type Workspace struct {
	ID                   string
	CreationID           string
	CheckpointRepository string
	Phase                string
	ExplicitQuotaBytes   uint64 `json:",omitempty"`
	Members              []WorkspaceMember
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
