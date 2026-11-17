package freshcleanup

import (
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

var key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var id = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var hex40 = regexp.MustCompile(`^[a-f0-9]{40}$`)

func rootPath(p payloadpolicy.Policy, key string) string {
	return filepath.Join(p.DataRoot, "benches", key)
}
func validPath(p string) bool {
	return len(p) <= 4096 && utf8.ValidString(p) && !strings.ContainsRune(p, 0) && fs.ValidPath(p) && p != "."
}

func (a Approval) validate(p payloadpolicy.Policy, expected Binding, owner Owner, assignment Assignment) error {
	policy, err := p.Bytes()
	b := a.Binding
	if err != nil || a.Version != Version || b != expected || a.Owner != owner || a.Assignment != assignment || !daemon(p, owner) || !validAssignment(assignment) || !key.MatchString(b.Key) || !id.MatchString(b.ID) || !b.Operation.Valid() || !hex64.MatchString(b.SpecSHA256) || !hex64.MatchString(b.IntentSHA256) || b.PolicySHA256 != digest(policy) || b.RootDevice == 0 || b.RootInode == 0 || a.RootPath != rootPath(p, b.Key) || !payloadpolicy.Directory(a.RootPath) {
		return ErrApproval
	}
	o := a.Observation
	parent := o.Parent
	if parent.Device != b.RootDevice || parent.Inode == 0 || parent.Inode == b.RootInode || parent.UID != 0 || os.FileMode(parent.Mode)&os.ModeType != os.ModeDir || parent.Mode & ^uint32(os.ModeDir|0777) != 0 || os.FileMode(parent.Mode).Perm()&0022 != 0 {
		return ErrApproval
	}
	if o.Tree.Root.Path != "." || o.Tree.Root.Device != b.RootDevice || o.Tree.Root.Inode != b.RootInode || os.FileMode(o.Tree.Root.Mode) != os.ModeDir|0700 || len(o.Tree.Entries) > MaxEntries || len(o.Owners) != len(o.Tree.Entries)+1 {
		return ErrApproval
	}
	entries := append([]reap.Entry{o.Tree.Root}, o.Tree.Entries...)
	seen := map[[2]uint64]bool{{parent.Device, parent.Inode}: true}
	paths := map[string]reap.Entry{}
	children := map[string][]string{}
	for i, e := range entries {
		if i > 0 && !validPath(e.Path) {
			return ErrApproval
		}
		if _, exists := paths[e.Path]; exists {
			return ErrApproval
		}
		if err := validEntry(e, o.Owners[i], owner, b.RootDevice); err != nil {
			return err
		}
		node := [2]uint64{e.Device, e.Inode}
		if seen[node] {
			return ErrApproval
		}
		seen[node] = true
		paths[e.Path] = e
		if i > 0 {
			parent := path.Dir(e.Path)
			children[parent] = append(children[parent], e.Path)
		}
	}
	for name := range children {
		e, exists := paths[name]
		if !exists || !os.FileMode(e.Mode).IsDir() {
			return ErrApproval
		}
		sort.Strings(children[name])
	}
	order := make([]string, 0, len(entries))
	var visit func(string)
	visit = func(name string) {
		order = append(order, name)
		for _, child := range children[name] {
			visit(child)
		}
	}
	visit(".")
	if len(order) != len(entries) {
		return ErrApproval
	}
	for i, e := range entries {
		if order[i] != e.Path {
			return ErrApproval
		}
	}
	return nil
}

// daemon accepts only the policy's nonzero daemon identity as the cleaned owner.
func daemon(p payloadpolicy.Policy, o Owner) bool {
	return o.UID != 0 && o.GID != 0 && o.UID == p.DaemonUID && o.GID == p.DaemonGID
}

// validAssignment requires a Fresh project ID and whole-KiB, nonzero caps.
func validAssignment(a Assignment) bool {
	return a.ProjectID >= 10000 && a.Bytes >= 1024 && a.Bytes%1024 == 0 && a.Inodes != 0
}

// validEntry binds one entry to its observed node and the single owner. The
// daemon-uid worker must traverse, re-hash and unlink everything it approved:
// a directory needs owner rwx and a regular file owner read, or a retained
// approval would wedge on EACCES.
func validEntry(e reap.Entry, owner Ownership, want Owner, device uint64) error {
	mode := os.FileMode(e.Mode)
	if owner.Path != e.Path || owner.Device != e.Device || owner.Inode != e.Inode || owner.UID != want.UID || owner.GID != want.GID || owner.Nlink == 0 || e.Device != device || e.Inode == 0 || e.Recoverable || e.Mode & ^uint32(os.ModeDir|os.ModeSymlink|0777) != 0 {
		return ErrApproval
	}
	switch mode & os.ModeType {
	case os.ModeDir:
		if e.SHA256 != digest(nil) || e.GitSHA1 != "" || e.GitSHA256 != "" || mode.Perm()&0700 != 0700 {
			return ErrApproval
		}
	case 0:
		if mode.Perm()&0400 == 0 {
			return ErrApproval
		}
		fallthrough
	case os.ModeSymlink:
		if owner.Nlink != 1 || !hex64.MatchString(e.SHA256) || !hex40.MatchString(e.GitSHA1) || !hex64.MatchString(e.GitSHA256) {
			return ErrApproval
		}
	default:
		return ErrApproval
	}
	return nil
}

// Validate compares the complete approval to the caller's independently retained
// binding. It proves only model consistency, not live storage or authorization.
func (a Approval) Validate(p payloadpolicy.Policy, b Binding, owner Owner, assignment Assignment) error {
	if err := a.validate(p, b, owner, assignment); err != nil {
		return err
	}
	raw, err := a.digestBytes()
	if err != nil || a.SHA256 != digest(raw) {
		return ErrApproval
	}
	encoded, err := json.Marshal(a)
	if err != nil || len(encoded) > MaxApproval {
		return ErrApproval
	}
	return nil
}
