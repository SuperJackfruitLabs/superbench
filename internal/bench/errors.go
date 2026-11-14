package bench

import "errors"

var (
	ErrArchived   = errors.New("bench is archived; recover its checkpoint under a new key")
	ErrNotFound   = errors.New("unknown bench")
	ErrBusy       = errors.New("bench already leased")
	ErrExists     = errors.New("bench already exists")
	ErrLease      = errors.New("valid lease token required")
	ErrDirty      = errors.New("bench has uncommitted or untracked files; commit or preserve them before checkpoint/release")
	ErrNotFenced  = errors.New("supervised execution is not fenced; run bench stop first")
	ErrCheckpoint = errors.New("checkpoint current HEAD before releasing")
)
