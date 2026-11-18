// Package freshgeneration names the generations of one bench key. A key's
// first Fresh bench is generation 0 (it names none); once it is reclaimed, or
// a create that never completed has had its allocation retired by root, a
// create on the key makes generation 2, then 3, and so on. Each generation has
// its own workspace ID, bootstrap intent, controls and quota project; an
// earlier one's evidence stays where it is and is never reused.
package freshgeneration

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// Max bounds the generations of one key.
const Max = 1 << 20

var identity = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ErrGeneration refuses a generation that does not follow its predecessor.
var ErrGeneration = errors.New("bench generation does not follow its reclaimed or retired predecessor")

// Later reports a valid later generation: 2 to Max, naming the different
// workspace ID it follows.
func Later(generation uint32, previous, id string) bool {
	return generation >= 2 && generation <= Max && identity.MatchString(previous) && previous != id
}

// Before is the generation a later one follows: 0 (the first) for 2.
func Before(generation uint32) uint32 {
	if generation <= 2 {
		return 0
	}
	return generation - 1
}

// Stem names a generation's retained files: the key itself for the first
// generation, whose names are unchanged, and KEY@N after it. A key never
// contains "@", so no stem names another key's files.
func Stem(key string, generation uint32) string {
	if generation == 0 {
		return key
	}
	return key + "@" + strconv.FormatUint(uint64(generation), 10)
}

// Prior is what a later generation reads of its predecessor's retained
// bootstrap intent.
type Prior struct {
	Key, ID    string
	Generation uint32
	Phase      string
	Authority  string // the storage authority its operation recorded
	Path       string // its recorded leaf; empty for a reserved intent
	ProjectID  uint32 // its recorded project; zero for a reserved intent
	// Bytes and Inodes complete its recorded allocation. Operation (its
	// canonical encoding) and PolicyDigest are what its operation recorded.
	Bytes, Inodes           uint64
	Operation, PolicyDigest string
	// IntentSHA256 and SpecSHA256 digest the retained intent's exact bytes
	// and its canonical spec, as the bootstrap inspection does.
	IntentSHA256, SpecSHA256 string
	// Retirement is root's retained retirement of the key's allocation, when
	// there is one (payload-retirements/KEY.json).
	Retirement *Retirement
}

// Allocation is a quota allocation as an intent or a retirement records it.
type Allocation struct {
	Path          string
	ProjectID     uint32
	Bytes, Inodes uint64
}

// Retirement is what a retained, reviewed retirement plan records: the
// inspection of the intent it retired, under a denied operation, and the
// storage approval whose allocation it made a tombstone. It is evidence only
// with the ledger's tombstone and the absent leaf, which the caller checks
// under the allocation lock.
type Retirement struct {
	Key, ID, Phase           string
	IntentSHA256, SpecSHA256 string
	Operation, PolicyDigest  string
	OperationDenied          bool
	Recorded                 Allocation // the intent's, as the plan inspected it
	ApprovalKey, Authority   string     // the storage approval's
	Approved                 Allocation // the approval's, now the tombstone
}

// Unfinished reports a bootstrap intent phase before completed: a create
// that never published its layout. Only such a generation can be retired by
// root's direct recovery rather than reclaimed.
func Unfinished(phase string) bool {
	switch phase {
	case "reserved", "allocating", "begun", "staging", "materialized_unfenced", "staged", "transferring":
		return true
	}
	return false
}

// Allocation is the project the predecessor held: its recorded allocation,
// or for a reserved intent, which records none, its retirement's.
// Only its leaf and project are named; its caps are not.
func (p Prior) Allocation() Allocation {
	if p.Phase == "reserved" && p.Retirement != nil {
		return Allocation{Path: p.Retirement.Approved.Path, ProjectID: p.Retirement.Approved.ProjectID}
	}
	return Allocation{Path: p.Path, ProjectID: p.ProjectID}
}

// Retired requires prior to be a generation that never completed whose
// allocation root retired: an unfinished intent under authority, and a
// retirement that reviewed exactly that intent (its bytes, spec, phase,
// workspace, operation, policy and recorded allocation, none if reserved)
// under its denied operation, and approved that allocation at root.
func Retired(prior Prior, authority, root string) error {
	r := prior.Retirement
	recorded := Allocation{prior.Path, prior.ProjectID, prior.Bytes, prior.Inodes}
	reserved := prior.Phase == "reserved"
	if r == nil || !Unfinished(prior.Phase) || prior.Authority != authority || !r.OperationDenied || r.Key != prior.Key || r.ApprovalKey != prior.Key || r.ID != prior.ID || r.Phase != prior.Phase ||
		r.IntentSHA256 == "" || r.IntentSHA256 != prior.IntentSHA256 || r.SpecSHA256 == "" || r.SpecSHA256 != prior.SpecSHA256 ||
		r.Operation == "" || r.Operation != prior.Operation || r.PolicyDigest == "" || r.PolicyDigest != prior.PolicyDigest ||
		r.Recorded != recorded || reserved && recorded != (Allocation{}) || !reserved && r.Approved != recorded ||
		r.Authority != authority || r.Approved.Path != root || r.Approved.ProjectID < 10000 {
		return ErrGeneration
	}
	return nil
}

// Follows requires prior to be the generation right before generation of
// key, with workspace ID previous, under the same storage authority and at
// the same leaf root, holding a Fresh project: either reclaimed, or never
// completed and retired by root (Retired).
func Follows(prior Prior, key, previous string, generation uint32, authority, root string) error {
	if generation == 0 || prior.Key != key || prior.ID != previous || prior.Generation != Before(generation) {
		return ErrGeneration
	}
	if prior.Phase == "reclaimed" {
		if prior.Authority != authority || prior.Path != root || prior.ProjectID < 10000 {
			return ErrGeneration
		}
		return nil
	}
	return Retired(prior, authority, root)
}

// LayoutAbsent refuses, before any effect, while the daemon's layout registry
// in state still publishes a layout under key. Publication creates a key's
// registration exclusively, so a later generation's would be refused only
// after its tree was transferred. The daemon moves the reclaimed generation's
// registration aside first. This is an early check; publication is the fence.
// A predecessor that never completed published none.
func LayoutAbsent(state, key string) error {
	_, err := os.Lstat(filepath.Join(state, "payload-layouts", key+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.Join(ErrGeneration, errors.New("the reclaimed generation's layout registration is still published"), err)
}
