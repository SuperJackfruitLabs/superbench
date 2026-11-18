// Package freshrecovery holds the direct-root operator steps that bring a failed
// Fresh allocation back to an absent leaf: removing an empty leaf (with the fixed
// directories Begin creates) and setting the never-set quota limits of a reserved
// project. Each step is plan, review, then effect under a retained approval.
// Root never deletes a file, never deletes recursively and never adopts storage.
// Reclaim is the one step the daemon starts: it reaches it through the fixed
// helper's sudo route, bound to its own reserved reclaim operation, and the
// payload account deletes the tree. Its threat model: root trusts the daemon
// that the bench's work is checkpointed, because that evidence lives in the
// daemon-owned mirror and remote, which root cannot observe. Root checks
// itself that the request names a completed bootstrap's own leaf and
// allocation under the workspace's reclaim identity. Its "not in use" check
// (ErrReclaimBusy) is point in time. It attributes an open file to the bench
// by project ID, which holds because every payload process runs under the
// payload filter that refuses project and inherit-flag changes, and root
// refuses (ErrReclaimEscaped) a tree where one happened anyway. The
// guarantees are the backstops after
// the purge: root removes only an empty leaf, and retires the project only at
// zero usage. Only cmd/bench-quota may import this package.
package freshrecovery

import (
	"errors"
	"path"
	"strconv"
	"strings"
)

// ErrRecovery reports a refused recovery step. Other errors come from the system.
var ErrRecovery = errors.New("fresh allocation recovery refused")

const (
	modeType = 0o170000
	modeDir  = 0o040000
	// fsIocFsgetxattr is _IOR('X', 31, struct fsxattr) on amd64 and arm64.
	fsIocFsgetxattr = uintptr(2<<30 | fsxattrSize<<16 | 'X'<<8 | 31)
	fsxattrSize     = 28
)

// fsxattr mirrors the kernel's struct fsxattr.
type fsxattr struct {
	Xflags, Extsize, Nextents, Projid, Cowextsize uint32
	Pad                                           [8]byte
}

// Node is one directory's identity: Mode is the raw st_mode, type bits included.
type Node struct {
	Device, Inode  uint64
	Mode, UID, GID uint32
}

// Entry is one fixed directory Begin creates, by its path relative to the leaf.
type Entry struct {
	Name string
	Node Node
}

// Leaf is the observed fixed leaf: its node, its XFS project ID and the fixed
// directories present beneath it, in creation order.
type Leaf struct {
	Node      Node
	ProjectID uint32
	Fixed     []Entry
}

// owners are the two owners the phase table allows.
type owners struct{ daemonUID, daemonGID uint32 }

func (o owners) root(n Node) bool   { return n.UID == 0 && n.GID == 0 }
func (o owners) daemon(n Node) bool { return n.UID == o.daemonUID && n.GID == o.daemonGID }

// private reports a directory with exactly mode 0700 and no special bits.
func private(n Node) bool {
	return n.Mode&modeType == modeDir && n.Mode&0o7777 == 0o700 && n.Inode != 0
}

// leafAllowed applies the phase table to an observed leaf. recorded is the
// intent's recorded root (zero for reserved), assigned the selected project ID,
// device the pinned benches parent's device and fixed Begin's creation order.
func leafAllowed(phase string, recorded [2]uint64, assigned uint32, o owners, device uint64, fixed []string, l Leaf) error {
	n := l.Node
	if !private(n) || n.Device != device || o.daemonUID == 0 || o.daemonGID == 0 {
		return ErrRecovery
	}
	atRecorded := atRecordedRoot(recorded, n)
	switch phase {
	case "reserved":
		// The crash fell between mkdir and project -s, or before the intent advanced.
		if recorded != [2]uint64{} || !o.root(n) || l.ProjectID != 0 && l.ProjectID != assigned || len(l.Fixed) != 0 {
			return ErrRecovery
		}
		return nil
	case "allocating":
		if !atRecorded || l.ProjectID != assigned {
			return ErrRecovery
		}
		if o.root(n) {
			return fixedPrefix(fixed, l.Fixed, n.Device, o)
		}
		// (c): the leaf is daemon-owned. Its contents need the daemon-uid cleanup.
		if !o.daemon(n) || len(l.Fixed) != 0 {
			return ErrRecovery
		}
		return nil
	case "begun", "staging", "materialized_unfenced", "staged":
		if !atRecorded || l.ProjectID != assigned || !o.daemon(n) || len(l.Fixed) != 0 {
			return ErrRecovery
		}
		return nil
	}
	return ErrRecovery
}

// atRecordedRoot reports a node at the intent's recorded root inode. The
// recorded device is not compared: it was this boot's st_dev when the intent was
// written, and a loop or block device can be renumbered at boot, exactly when a
// crash leaves recovery to do. The node's device is compared instead with the
// benches parent the selection pinned in this boot, and the selection's
// authority binds the data volume by its UUID.
func atRecordedRoot(recorded [2]uint64, n Node) bool {
	return recorded != [2]uint64{} && recorded[1] == n.Inode
}

// fixedPrefix accepts only a prefix of Begin's fixed directories: each a private
// directory on the leaf's device, daemon-owned except the last, which may still
// be root-owned because Begin creates each one before it changes the owner.
func fixedPrefix(fixed []string, present []Entry, device uint64, o owners) error {
	if len(present) > len(fixed) {
		return ErrRecovery
	}
	seen := map[[2]uint64]bool{}
	for k, e := range present {
		n := e.Node
		last := k == len(present)-1
		if e.Name != fixed[k] || !private(n) || n.Device != device || seen[[2]uint64{n.Device, n.Inode}] || !o.daemon(n) && !(last && o.root(n)) {
			return ErrRecovery
		}
		seen[[2]uint64{n.Device, n.Inode}] = true
	}
	return nil
}

// children maps each directory that must exist ("." is the leaf) to the fixed
// names directly beneath it, given the fixed prefix that is present.
func children(present []string) map[string][]string {
	result := map[string][]string{".": nil}
	for _, name := range present {
		result[name] = nil
	}
	for _, name := range present {
		parent := path.Dir(name)
		result[parent] = append(result[parent], path.Base(name))
	}
	return result
}

// prefixOf reports whether observed is a prefix of planned, entry for entry.
func prefixOf(observed, planned []Entry) bool {
	if len(observed) > len(planned) {
		return false
	}
	for k := range observed {
		if observed[k] != planned[k] {
			return false
		}
	}
	return true
}

// rows classifies one xfs_quota report for a project: no row at all, or exactly
// one row with zero usage, the original hard cap and a zero soft cap. Anything
// else, including any line for another project, is refused.
type rows int

const (
	noRow rows = iota
	exactRow
)

// classify reads one report. No row is accepted only when the tool also wrote nothing to standard error:
// an empty report beside a diagnostic is a failure, not a missing project.
func classify(raw, diagnostic []byte, id uint32, hard uint64) (rows, error) {
	want := "#" + strconv.FormatUint(uint64(id), 10)
	found := noRow
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] != want || len(f) < 4 || found != noRow {
			return noRow, ErrRecovery
		}
		used, err := strconv.ParseUint(f[1], 10, 64)
		soft, softErr := strconv.ParseUint(f[2], 10, 64)
		cap, capErr := strconv.ParseUint(f[3], 10, 64)
		if err != nil || softErr != nil || capErr != nil || used != 0 || soft != 0 || cap != hard {
			return noRow, ErrRecovery
		}
		found = exactRow
	}
	if found == noRow && len(diagnostic) != 0 {
		return noRow, ErrRecovery
	}
	return found, nil
}

// quotaOn requires project accounting and enforcement both reported ON, once.
func quotaOn(raw []byte) error {
	var accounting, enforcement int
	for _, line := range strings.Split(string(raw), "\n") {
		switch strings.TrimSpace(line) {
		case "Accounting: ON":
			accounting++
		case "Enforcement: ON":
			enforcement++
		case "Accounting: OFF", "Enforcement: OFF":
			return ErrRecovery
		}
	}
	if accounting != 1 || enforcement != 1 {
		return ErrRecovery
	}
	return nil
}

// mountIdentity requires findmnt's TARGET FSROOT UUID for a dedicated mount of root.
func mountIdentity(raw []byte, root, uuid string) error {
	f := strings.Fields(string(raw))
	if len(f) != 3 || f[0] != root || f[1] != "/" || uuid == "" || !strings.EqualFold(f[2], uuid) {
		return ErrRecovery
	}
	return nil
}

// limitCommand is the only quota change this package makes.
func limitCommand(id uint32, bytes, inodes uint64) string {
	return "limit -p bhard=" + strconv.FormatUint(bytes/1024, 10) + "k ihard=" + strconv.FormatUint(inodes, 10) + " " + strconv.FormatUint(uint64(id), 10)
}
