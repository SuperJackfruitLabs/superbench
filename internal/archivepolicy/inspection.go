package archivepolicy

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"os"
	"path/filepath"
	"strings"
)

type Inspector struct {
	Data            string
	Git             func(string, ...string) (string, error)
	MirrorPath      func(string) string
	Clean           func(*model.Bench) error
	Confirmed       func(*model.Bench, string) error
	CheckpointError error
	ValidKey        func(string) bool
}

func (i Inspector) Paths(b *model.Bench) error {
	if !i.ValidKey(b.Key) || b.Path != filepath.Join(i.Data, "benches", b.Key) || b.Mirror != i.MirrorPath(b.Repo) {
		return errors.New("bench paths conflict with store")
	}
	actual, e := filepath.EvalSymlinks(b.Path)
	if e == nil {
		data, e := filepath.EvalSymlinks(i.Data)
		if e != nil {
			return e
		}
		if actual != filepath.Join(data, "benches", b.Key) {
			return errors.New("bench path escapes its data directory")
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}
func (i Inspector) Remote(b *model.Bench) error {
	origin, e := i.Git(b.Mirror, "remote", "get-url", "origin")
	if e != nil {
		return e
	}
	if origin != b.Repo {
		return errors.New("mirror origin changed")
	}
	copy := *b
	copy.Path = b.Mirror
	return i.Confirmed(&copy, b.Checkpoint)
}
func (i Inspector) Checkout(b *model.Bench) error {
	if e := i.Clean(b); e != nil {
		return e
	}
	sha, e := i.Git(b.Path, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	if b.Checkpoint == "" || sha != b.Checkpoint {
		return i.CheckpointError
	}
	branch, e := i.Git(b.Path, "symbolic-ref", "--quiet", "HEAD")
	if e != nil {
		return e
	}
	if branch != "refs/heads/"+b.Branch {
		return i.CheckpointError
	}
	common, e := i.Git(b.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return e
	}
	actual, e := filepath.EvalSymlinks(common)
	if e != nil {
		return e
	}
	expected, e := filepath.EvalSymlinks(b.Mirror)
	if e != nil {
		return e
	}
	if actual != expected {
		return errors.New("worktree belongs to another mirror")
	}
	admin, e := i.Git(b.Path, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return e
	}
	if _, e = os.Lstat(filepath.Join(admin, "locked")); e == nil {
		return errors.New("worktree is locked; review its lock before archive")
	} else if !os.IsNotExist(e) {
		return e
	}
	return i.Remote(b)
}

func (i Inspector) Plan(b *model.Bench) (*reap.Plan, error) {
	// Compare raw file/link content with HEAD objects, rather than assuming an
	// index flag or a checkout filter proves that these local bytes recover.
	output, e := i.Git(b.Path, "ls-tree", "-r", "-z", "HEAD")
	rawFiles := []byte(output)
	if e != nil {
		return nil, e
	}
	objects := map[string]string{}
	for _, row := range strings.Split(string(rawFiles), "\x00") {
		header, path, ok := strings.Cut(row, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(header)
		if len(fields) == 3 && fields[1] == "blob" {
			objects[path] = fields[2]
		}
	}
	tree, e := reap.Snapshot(b.Path, nil)
	if e != nil {
		return nil, e
	}
	for i := range tree.Entries {
		v := &tree.Entries[i]
		oid, ok := objects[v.Path]
		v.Recoverable = ok && (oid == v.GitSHA1 || oid == v.GitSHA256)
	}
	p := &reap.Plan{Version: "superbench.reap.v3", FreezeDigest: b.ArchiveFreezeDigest, ReapAfter: b.ReapAfter, Key: b.Key, Path: b.Path, Checkpoint: b.Checkpoint, Tree: tree}
	p.Digest, e = p.DigestValue()
	return p, e
}
