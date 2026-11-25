package model

import (
	"encoding/hex"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

type Execution = execution.Execution
type Lease struct {
	ID            string `json:",omitempty"` // Opaque, per acquisition; kept across rotations.
	Generation    uint64 `json:",omitempty"` // 1 on acquisition, +1 on every token rotation.
	Token         string
	Holder        string
	Execution     *Execution `json:",omitempty"`
	Continuation  string     `json:",omitempty"`
	RetainedRuns  []string   `json:",omitempty"`
	StopRequested bool       `json:",omitempty"`
}
type Bench struct {
	RegistrationID       string `json:",omitempty"`
	PayloadLayoutVersion string `json:",omitempty"`
	AllocationRoot       string `json:",omitempty"`
	Key                  string
	Repo                 string
	Branch               string
	Path                 string
	Mirror               string
	Checkpoint           string
	CreatedFrom          string                `json:",omitempty"`
	ArchivedAt           string                `json:",omitempty"`
	ReapAfter            string                `json:",omitempty"`
	ArchiveFreezeDigest  string                `json:",omitempty"`
	ArchiveFrozenAt      string                `json:",omitempty"`
	ArchiveMerge         *ArchiveMergeEvidence `json:",omitempty"`
	ArchiveUnmerged      *ArchiveMergeEvidence `json:",omitempty"`
	ReapingDigest        string                `json:",omitempty"`
	ReapedAt             string                `json:",omitempty"`
	ProjectID            uint32
	QuotaBytes           uint64
	RequestedQuotaBytes  uint64 `json:",omitempty"`
	QuotaInodes          uint64
	Lease                *Lease     `json:",omitempty"`
	Workspace            *Workspace `json:",omitempty"`
}

// ArchiveMergeEvidence is an observation, not a permanent claim about remote refs.
// The containing archive record binds it to the repository, task and freeze intent.
type ArchiveMergeEvidence struct {
	Checkpoint, ConfigDigest, TargetBranch, TargetCommit, ObservedAt string
}

type Options struct{ Repo, Branch, Base, Holder, Token string }

func ValidCommit(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && (len(raw) == 20 || len(raw) == 32)
}

var ErrDirty = errors.New("bench has uncommitted or untracked files; commit or preserve them before checkpoint/release")
