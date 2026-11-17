// Package freshcleanup models an explicit bounded cleanup approval. It performs
// no filesystem inspection, credential change, deletion or quota retirement.
// A maintenance caller must independently establish the original retained
// provenance, permanent denial and current installed/controller authority.
package freshcleanup

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

const Version = "superbench.fresh-cleanup-approval.v1"
const MaxApproval = reap.MaxManifest
const MaxEntries = 65536

var ErrApproval = errors.New("fresh cleanup approval refused")

// Binding pins evidence supplied by the maintenance owner from an independently
// validated original intent. Valid syntax does not establish that evidence or
// assert that the operation is denied, the tree is current, or writers stopped.
type Binding struct {
	Key, ID, PolicySHA256, SpecSHA256, IntentSHA256 string
	Operation                                       quotaoperation.Identity
	RootDevice, RootInode                           uint64
}

type Parent struct {
	Device, Inode  uint64
	Mode, UID, GID uint32
}

// Ownership binds each content entry to a separately observed physical owner.
// Regular files and symlinks must have exactly one link. Directory link counts
// are initial observations only: deleting approved children can change them.
// They must not become a static directory-link-count invariant during retries.
type Ownership struct {
	Path                 string
	Device, Inode, Nlink uint64
	UID, GID             uint32
}

// Observation contains a complete initial inventory. Tree and Owners use the
// existing reap traversal: parent before children, lexically sorted siblings.
// This is not an executable grant, and no current filesystem proof is inferred
// from caller-supplied metadata or a successfully decoded approval.
type Observation struct {
	Parent Parent
	Tree   reap.Tree
	Owners []Ownership
}

// Owner is the single uid and gid that must own the root and every entry. The
// caller derives it from the retained phase and the observed leaf; the model
// accepts only the policy's daemon, never root or the payload identity.
type Owner struct{ UID, GID uint32 }

// Assignment is the selected quota reservation the cleaned tree belongs to.
type Assignment struct {
	ProjectID     uint32
	Bytes, Inodes uint64
}

type Approval struct {
	Version     string
	Binding     Binding
	Owner       Owner
	Assignment  Assignment
	RootPath    string
	Observation Observation
	SHA256      string
}

// New copies an already-observed inventory; it never snapshots or adopts a tree.
// owner and assignment are the caller's independently derived expectations.
func New(p payloadpolicy.Policy, b Binding, owner Owner, assignment Assignment, o Observation) (Approval, error) {
	if len(o.Tree.Entries) > MaxEntries || len(o.Owners) != len(o.Tree.Entries)+1 {
		return Approval{}, ErrApproval
	}
	a := Approval{Version: Version, Binding: b, Owner: owner, Assignment: assignment, RootPath: rootPath(p, b.Key), Observation: o}
	a.Observation.Tree.Entries = append([]reap.Entry(nil), o.Tree.Entries...)
	a.Observation.Owners = append([]Ownership(nil), o.Owners...)
	if err := a.validate(p, b, owner, assignment); err != nil {
		return Approval{}, err
	}
	raw, err := a.digestBytes()
	if err != nil {
		return Approval{}, ErrApproval
	}
	a.SHA256 = digest(raw)
	if _, err = a.Bytes(p, b, owner, assignment); err != nil {
		return Approval{}, err
	}
	return a, nil
}
