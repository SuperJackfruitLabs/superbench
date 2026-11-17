// Package cleanworker is the unprivileged child that bench-quota re-executes to
// observe or delete the contents of a Fresh tree. Root never opens the tree's
// files or deletes them: it starts this worker with the credentials of the
// tree's owner, sends one bounded request frame on stdin and reads one bounded
// reply frame from stdout, which it treats as data. The owner is the daemon
// for a leaked bootstrap tree (observe, delete) and the payload account for a
// reclaimed bench (purge). The worker checks its own credentials and
// descriptors before it reads anything. It holds no policy, ledger, quota or
// journal authority, and only cmd/bench-quota may import it.
package cleanworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

const Version = "superbench.clean-worker.v1"

// The modes. Observe is read-only; delete removes only approved contents;
// purge empties a payload-owned tree whose contents nobody reviewed.
const (
	Observe = "observe"
	Delete  = "delete"
	Purge   = "purge"
)

// Command is the argument that selects the worker in bench-quota's main. It is
// dispatched before any flag or policy parsing.
const Command = "clean-worker"

// Ready is the worker's first output, written only after every self-check has
// passed. Root sends the request only after reading exactly this line.
const Ready = "superbench.clean-worker.ready.v1\n"

// MaxFrame bounds one request or reply: an approved tree plus per-entry owners.
const MaxFrame = 2 * reap.MaxManifest

// ErrWorker reports a refused worker frame or self-check.
var ErrWorker = errors.New("clean worker refused")

// Request is root's single frame. Approved is set only for delete, Target
// only for purge.
type Request struct {
	Version, Mode, Root string
	Approved            *reap.Tree
	Target              *Target `json:",omitempty"`
}

// Target is the tree a purge empties: the root directory's node, which the
// worker must find at Root, and the XFS project every directory beneath it
// must carry. A directory on another device or in another project is never
// entered, so the purge cannot leave the tree.
type Target struct {
	Device, Inode uint64
	ProjectID     uint32
}

// Owner is one entry's physical owner, link count and raw Go file mode, from an
// fd-relative Lstat beneath an os.Root, in the tree's order (the root first).
type Owner struct {
	Path                 string
	Device, Inode, Nlink uint64
	Mode, UID, GID       uint32
}

// Reply is the worker's single frame. Tree and Owners are set only for observe.
type Reply struct {
	Version string
	Tree    *reap.Tree
	Owners  []Owner
}

func validRoot(root string) bool {
	return len(root) <= 4096 && utf8.ValidString(root) && !strings.ContainsRune(root, 0) && filepath.IsAbs(root) && filepath.Clean(root) == root && root != "/"
}

func (r Request) valid() bool {
	if r.Version != Version || !validRoot(r.Root) {
		return false
	}
	switch r.Mode {
	case Observe:
		return r.Approved == nil && r.Target == nil
	case Delete:
		return r.Approved != nil && r.Target == nil
	case Purge:
		// Fixed-policy projects start at 10000; zero is the filesystem default.
		return r.Approved == nil && r.Target != nil && r.Target.Device != 0 && r.Target.Inode != 0 && r.Target.ProjectID >= 10000
	}
	return false
}

// Valid checks a reply for mode: an observation must give exactly one owner
// per entry, root first, each naming the same path, node and mode as the tree.
func (r Reply) Valid(mode string) error {
	if r.Version != Version {
		return ErrWorker
	}
	switch mode {
	case Delete, Purge:
		if r.Tree != nil || r.Owners != nil {
			return ErrWorker
		}
		return nil
	case Observe:
		if r.Tree == nil || len(r.Owners) != len(r.Tree.Entries)+1 {
			return ErrWorker
		}
		for i, e := range append([]reap.Entry{r.Tree.Root}, r.Tree.Entries...) {
			o := r.Owners[i]
			if o.Path != e.Path || o.Device != e.Device || o.Inode != e.Inode || o.Mode != e.Mode {
				return ErrWorker
			}
		}
		return nil
	}
	return ErrWorker
}

// EncodeRequest and EncodeReply produce the only accepted (compact) encoding.
func EncodeRequest(r Request) ([]byte, error) {
	if !r.valid() {
		return nil, ErrWorker
	}
	return encode(r)
}

func EncodeReply(r Reply, mode string) ([]byte, error) {
	if r.Valid(mode) != nil {
		return nil, ErrWorker
	}
	return encode(r)
}

// DecodeRequest and DecodeReply accept only a bounded, canonical frame, so no
// unknown, duplicate or reordered field survives.
func DecodeRequest(raw []byte) (Request, error) {
	var r Request
	if canonical(raw, &r) != nil || !r.valid() {
		return Request{}, ErrWorker
	}
	return r, nil
}

func DecodeReply(raw []byte, mode string) (Reply, error) {
	var r Reply
	if canonical(raw, &r) != nil || r.Valid(mode) != nil {
		return Reply{}, ErrWorker
	}
	return r, nil
}

func encode(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > MaxFrame {
		return nil, ErrWorker
	}
	return raw, nil
}

func canonical(raw []byte, v any) error {
	if len(raw) > MaxFrame || !utf8.Valid(raw) || json.Unmarshal(raw, v) != nil {
		return ErrWorker
	}
	again, err := json.Marshal(v)
	if err != nil || !bytes.Equal(again, raw) {
		return ErrWorker
	}
	return nil
}
