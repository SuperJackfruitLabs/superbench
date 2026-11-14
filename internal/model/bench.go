package model

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
)

type Execution = execution.Execution
type Lease struct {
	Token         string
	Holder        string
	Execution     *Execution `json:",omitempty"`
	Continuation  string     `json:",omitempty"`
	RetainedRuns  []string   `json:",omitempty"`
	StopRequested bool       `json:",omitempty"`
}
type Bench struct {
	Key                 string
	Repo                string
	Branch              string
	Path                string
	Mirror              string
	Checkpoint          string
	ArchivedAt          string `json:",omitempty"`
	ReapAfter           string `json:",omitempty"`
	ArchiveFreezeDigest string `json:",omitempty"`
	ArchiveFrozenAt     string `json:",omitempty"`
	ReapingDigest       string `json:",omitempty"`
	ReapedAt            string `json:",omitempty"`
	ProjectID           uint32
	QuotaBytes          uint64
	RequestedQuotaBytes uint64 `json:",omitempty"`
	QuotaInodes         uint64
	Lease               *Lease `json:",omitempty"`
}

type Options struct{ Repo, Branch, Base, Holder, Token string }

var ErrDirty = errors.New("bench has uncommitted or untracked files; commit or preserve them before checkpoint/release")
