// Package quotaleaf detects a bench whose entries left its XFS quota project.
// Project quotas alone do not hold against a file's owner, who may change the
// project ID and clear the inherit flag; the payload filter prevents that, and
// these root-side checks refuse a bench where it happened anyway.
package quotaleaf

import "errors"

var (
	// ErrLeafProject: the bench's leaf directory has another project ID or
	// lacks the project-inherit flag.
	ErrLeafProject = errors.New("bench leaf is outside its quota project")
	// ErrTreeProject: an entry beneath the leaf is outside the project.
	ErrTreeProject = errors.New("bench tree entry is outside its quota project")
	// ErrTreeLimit: the tree holds more distinct inodes than the bench's
	// inode cap, which only entries charged elsewhere make possible.
	ErrTreeLimit = errors.New("bench tree holds more inodes than its project cap")
	// ErrTreeNames: the walk met more names than MaxNames. Hard links add
	// names without inodes, so this is a bound on work, not an escape.
	ErrTreeNames = errors.New("bench tree exceeds the project check's name bound")
)

// MaxNames bounds the names one walk visits, as the purge worker's step
// bound does.
const MaxNames = 1 << 26

// FlagProjInherit is FS_XFLAG_PROJINHERIT.
const FlagProjInherit = 0x200

// Attr is the part of struct fsxattr the checks read.
type Attr struct{ Xflags, ProjectID uint32 }

// Check requires the project ID and, for a directory, the inherit flag that
// gives new children the same project.
func Check(a Attr, project uint32, directory bool) bool {
	return project != 0 && a.ProjectID == project && (!directory || a.Xflags&FlagProjInherit != 0)
}

// budget is one walk's bounds: distinct inodes against the bench's inode cap
// and names against the name bound. Hard links add names, not inodes.
type budget struct {
	seen          map[[2]uint64]bool
	inodes, names int
}

func newBudget(inodes, names int) *budget {
	return &budget{seen: map[[2]uint64]bool{}, inodes: inodes, names: names}
}

// inode counts a node once however many names reach it.
func (b *budget) inode(dev, ino uint64) error {
	id := [2]uint64{dev, ino}
	if b.seen[id] {
		return nil
	}
	if b.seen[id] = true; len(b.seen) > b.inodes {
		return ErrTreeLimit
	}
	return nil
}

// name counts n more names.
func (b *budget) name(n int) error {
	if b.names -= n; b.names < 0 {
		return ErrTreeNames
	}
	return nil
}
