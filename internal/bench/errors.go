package bench

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

var (
	ErrArchived   = errors.New("bench is archived; recover its checkpoint under a new key")
	ErrNotFound   = errors.New("unknown bench")
	ErrBusy       = errors.New("bench already leased")
	ErrExists     = errors.New("bench already exists")
	ErrLease      = errors.New("valid lease token required")
	ErrDirty      = model.ErrDirty
	ErrNotFenced  = errors.New("supervised execution is not fenced; run bench stop first")
	ErrCheckpoint = errors.New("checkpoint current HEAD before releasing")
)
