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
	// ErrTreeLimit: the tree has more entries or depth than the walk allows.
	ErrTreeLimit = errors.New("bench tree exceeds the project check limit")
)

// FlagProjInherit is FS_XFLAG_PROJINHERIT.
const FlagProjInherit = 0x200

// Attr is the part of struct fsxattr the checks read.
type Attr struct{ Xflags, ProjectID uint32 }

// Check requires the project ID and, for a directory, the inherit flag that
// gives new children the same project.
func Check(a Attr, project uint32, directory bool) bool {
	return project != 0 && a.ProjectID == project && (!directory || a.Xflags&FlagProjInherit != 0)
}
