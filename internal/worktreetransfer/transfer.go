// Package worktreetransfer relinquishes a fenced checkout's symbolic branch
// ownership without changing its index, files, or worktree registration.
package worktreetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const Version = "superbench.worktree-transfer.v1"
const maxBytes = 1 << 40
const maxEntries = 1000000

type Manager struct {
	Git func(context.Context, string, ...string) (string, error)
	// Gate proves durable approval and exclusion of source writers. The caller
	// must retain that exclusion throughout Apply, including failed calls.
	Gate    func(context.Context, Plan) error
	Timeout time.Duration
}
type Identity struct {
	Device, Inode uint64
	Mode          uint32
}
type Entry struct {
	Path         string
	Identity     Identity
	Size         int64
	Digest, Link string
}
type Plan struct {
	Version, Checkout, Common, Admin, TaskRef, Commit, ObjectFormat string
	CheckoutIdentity, CommonIdentity, AdminIdentity                 Identity
	Metadata                                                        []Entry
	GitFile                                                         Entry
	TreeDigest                                                      string
	TreeEntries                                                     uint64
	TreeBytes                                                       uint64
}

func (m Manager) bounded(ctx context.Context) (context.Context, context.CancelFunc, error) {
	t := m.Timeout
	if t == 0 {
		t = 2 * time.Minute
	}
	if t < 0 || t > time.Hour || m.Git == nil {
		return nil, nil, errors.New("invalid transfer Git adapter or timeout")
	}
	c, cancel := context.WithTimeout(ctx, t)
	return c, cancel, nil
}
func (m Manager) Inspect(ctx context.Context, checkout, common, taskRef, commit string) (Plan, error) {
	c, cancel, e := m.bounded(ctx)
	if e != nil {
		return Plan{}, e
	}
	defer cancel()
	return m.inspect(c, checkout, common, taskRef, commit, false)
}

// InspectDetached verifies a source registration after ownership relinquishment.
// It does not prove that the source was fenced or authorized for transfer.
func (m Manager) InspectDetached(ctx context.Context, checkout, common, taskRef, commit string) (Plan, error) {
	c, cancel, e := m.bounded(ctx)
	if e != nil {
		return Plan{}, e
	}
	defer cancel()
	return m.inspect(c, checkout, common, taskRef, commit, true)
}
func physical(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("transfer path is not normalized absolute")
	}
	p, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	if p != path {
		return errors.New("transfer path contains symlink")
	}
	return nil
}
func identity(path string) (Identity, error) {
	s, e := os.Lstat(path)
	if e != nil {
		return Identity{}, e
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("unsupported filesystem identity")
	}
	return Identity{uint64(st.Dev), uint64(st.Ino), uint32(s.Mode())}, nil
}
func (m Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	if e := ctx.Err(); e != nil {
		return "", e
	}
	out, e := m.Git(ctx, dir, args...)
	if len(out) > 1<<20 {
		return "", errors.New("transfer Git output exceeds bound")
	}
	return strings.TrimSpace(out), e
}

var oid = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (m Manager) inspect(ctx context.Context, checkout, common, ref, commit string, detached bool) (Plan, error) {
	p := Plan{Version: Version, Checkout: checkout, Common: common, TaskRef: ref, Commit: commit}
	if !strings.HasPrefix(ref, "refs/heads/") || !oid.MatchString(commit) {
		return p, errors.New("invalid transfer ref or commit")
	}
	for _, path := range []string{checkout, common} {
		if e := physical(path); e != nil {
			return p, e
		}
	}
	if _, e := m.git(ctx, common, "check-ref-format", ref); e != nil {
		return p, e
	}
	top, e := m.git(ctx, checkout, "rev-parse", "--show-toplevel")
	if e != nil || top != checkout {
		return p, errors.New("checkout root changed")
	}
	actual, e := m.git(ctx, checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil || actual != common {
		return p, errors.New("common Git directory changed")
	}
	p.Admin, e = m.git(ctx, checkout, "rev-parse", "--absolute-git-dir")
	if e != nil {
		return p, e
	}
	if e = physical(p.Admin); e != nil {
		return p, e
	}
	if filepath.Dir(p.Admin) != filepath.Join(common, "worktrees") {
		return p, errors.New("checkout is not a linked worktree")
	}
	if _, e = os.Lstat(filepath.Join(p.Admin, "locked")); !os.IsNotExist(e) {
		return p, errors.New("locked worktree cannot transfer")
	}
	head, e := readFixed(filepath.Join(p.Admin, "HEAD"))
	if e != nil {
		return p, e
	}
	expected := "ref: " + ref + "\n"
	if detached {
		expected = commit + "\n"
	}
	if string(head) != expected {
		return p, errors.New("unexpected worktree HEAD")
	}
	checks := [][]string{{"rev-parse", "--verify", "HEAD"}}
	if !detached {
		checks = append(checks, []string{"rev-parse", "--verify", ref})
	}
	for _, args := range checks {
		v, e := m.git(ctx, checkout, args...)
		if e != nil || v != commit {
			return p, errors.New("task ref or checkout commit changed")
		}
	}
	if kind, err := m.git(ctx, checkout, "cat-file", "-t", commit); err != nil || kind != "commit" {
		return p, errors.New("transfer source is not a commit")
	}
	p.ObjectFormat, e = m.git(ctx, checkout, "rev-parse", "--show-object-format")
	if e != nil {
		return p, e
	}
	if p.ObjectFormat != "sha1" && p.ObjectFormat != "sha256" || p.ObjectFormat == "sha1" && len(commit) != 40 || p.ObjectFormat == "sha256" && len(commit) != 64 {
		return p, errors.New("object format mismatch")
	}
	gitfile, e := readFixed(filepath.Join(checkout, ".git"))
	if e != nil || string(gitfile) != "gitdir: "+p.Admin+"\n" {
		return p, errors.New("worktree gitfile changed")
	}
	backlink, e := readFixed(filepath.Join(p.Admin, "gitdir"))
	if e != nil || string(backlink) != filepath.Join(checkout, ".git")+"\n" {
		return p, errors.New("registration backlink changed")
	}
	list, e := m.git(ctx, common, "worktree", "list", "--porcelain", "-z")
	if e != nil {
		return p, e
	}
	found := 0
	for _, block := range strings.Split(list, "\x00\x00") {
		fields := strings.Split(block, "\x00")
		if len(fields) == 0 || fields[0] != "worktree "+checkout {
			continue
		}
		found++
		want := "branch " + ref
		if detached {
			want = "detached"
		}
		has := false
		for _, f := range fields[1:] {
			if f == want {
				has = true
			}
			if strings.HasPrefix(f, "locked") || strings.HasPrefix(f, "prunable") {
				return p, errors.New("locked or prunable registration")
			}
		}
		if !has {
			return p, errors.New("registration ownership changed")
		}
	}
	if found != 1 {
		return p, errors.New("missing or ambiguous worktree registration")
	}
	p.CheckoutIdentity, e = identity(checkout)
	if e != nil {
		return p, e
	}
	p.CommonIdentity, e = identity(common)
	if e != nil {
		return p, e
	}
	p.AdminIdentity, e = identity(p.Admin)
	if e != nil {
		return p, e
	}
	budget := int64(maxBytes)
	config, e := snapshot(ctx, filepath.Join(common, "config"), "common/config", &budget)
	if e != nil {
		return p, e
	}
	if !os.FileMode(config.Identity.Mode).IsRegular() {
		return p, errors.New("common configuration is not regular")
	}
	p.Metadata = append(p.Metadata, config)
	for _, name := range []string{"gitdir", "commondir", "index"} {
		v, e := snapshot(ctx, filepath.Join(p.Admin, name), name, &budget)
		if e != nil {
			return p, e
		}
		if !os.FileMode(v.Identity.Mode).IsRegular() {
			return p, errors.New("worktree metadata is not regular")
		}
		p.Metadata = append(p.Metadata, v)
	}
	for _, name := range []string{"config.worktree", "HEAD.lock", "index.lock"} {
		path := filepath.Join(p.Admin, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			p.Metadata = append(p.Metadata, Entry{Path: name})
		} else if err != nil {
			return p, err
		} else if name != "config.worktree" {
			return p, errors.New("worktree metadata has a pending writer lock")
		} else {
			v, err := snapshot(ctx, path, name, &budget)
			if err != nil {
				return p, err
			}
			if !os.FileMode(v.Identity.Mode).IsRegular() {
				return p, errors.New("worktree configuration is not regular")
			}
			p.Metadata = append(p.Metadata, v)
		}
	}
	treeHash := sha256.New()
	encoder := json.NewEncoder(treeHash)
	e = filepath.WalkDir(checkout, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == checkout {
			return nil
		}
		if p.TreeEntries >= maxEntries {
			return errors.New("transfer snapshot entry bound exceeded")
		}
		rel, _ := filepath.Rel(checkout, path)
		v, e := snapshot(ctx, path, rel, &budget)
		if e != nil {
			return e
		}
		if rel == ".git" {
			p.GitFile = v
		}
		p.TreeEntries++
		if v.Digest != "" {
			p.TreeBytes += uint64(v.Size)
		}
		// Unix paths and symlink targets need not be UTF-8. Preserve their raw
		// bytes as well, rather than letting JSON replacement collapse names.
		if err := encoder.Encode(struct {
			Entry      Entry
			Path, Link []byte
		}{v, []byte(v.Path), []byte(v.Link)}); err != nil {
			return err
		}
		return nil
	})
	p.TreeDigest = hex.EncodeToString(treeHash.Sum(nil))
	return p, e
}
func snapshot(ctx context.Context, path, name string, budget *int64) (Entry, error) {
	v := Entry{Path: name}
	if e := ctx.Err(); e != nil {
		return v, e
	}
	s, e := os.Lstat(path)
	if e != nil {
		return v, e
	}
	v.Identity, e = identity(path)
	if e != nil {
		return v, e
	}
	v.Size = s.Size()
	switch {
	case s.IsDir():
		v.Size = 0
	case s.Mode()&os.ModeSymlink != 0:
		v.Link, e = os.Readlink(path)
	case s.Mode().IsRegular():
		if s.Size() > *budget {
			return v, errors.New("transfer snapshot byte bound exceeded")
		}
		f, err := openRegular(path)
		if err != nil {
			return v, err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil || !os.SameFile(s, opened) {
			return v, errors.New("snapshot file replaced")
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(contextReader{ctx, f}, s.Size()+1))
		if err != nil {
			return v, err
		}
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(s, after) || n != s.Size() || after.Size() != s.Size() || !after.ModTime().Equal(s.ModTime()) {
			return v, errors.New("snapshot file changed")
		}
		*budget -= n
		v.Digest = hex.EncodeToString(h.Sum(nil))
	default:
		return v, errors.New("unsupported checkout entry")
	}
	return v, e
}

type contextReader struct {
	context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// Apply requires the exact durably approved Plan. A successful detach is safe
// to retry; a failed post-effect gate leaves the detached checkout untouched.
func (m Manager) Apply(ctx context.Context, p Plan) error {
	c, cancel, e := m.bounded(ctx)
	if e != nil {
		return e
	}
	defer cancel()
	if m.Gate == nil {
		return errors.New("transfer requires approval gate")
	}
	if p.Version != Version {
		return errors.New("unsupported transfer plan")
	}
	if e = m.Gate(c, p); e != nil {
		return e
	}
	current, e := m.inspect(c, p.Checkout, p.Common, p.TaskRef, p.Commit, false)
	detached := false
	if e != nil {
		current, e = m.inspect(c, p.Checkout, p.Common, p.TaskRef, p.Commit, true)
		detached = true
	}
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(current, p) {
		return errors.New("transfer snapshot changed")
	}
	tip, e := m.git(c, p.Common, "rev-parse", "--verify", p.TaskRef)
	if e != nil || tip != p.Commit {
		return errors.New("task ref changed before transfer")
	}
	if e = m.Gate(c, p); e != nil {
		return e
	}
	var mutationErr error
	if !detached {
		_, mutationErr = m.git(c, p.Checkout, "update-ref", "--no-deref", "HEAD", p.Commit, p.Commit)
	}
	current, e = m.inspect(c, p.Checkout, p.Common, p.TaskRef, p.Commit, true)
	if e == nil && !reflect.DeepEqual(current, p) {
		e = errors.New("transfer changed protected snapshot")
	}
	// A failed Git call can have changed HEAD. Recheck authority even for that
	// ambiguous result; the gate is authorization, never a success notification.
	return errors.Join(mutationErr, e, m.Gate(c, p))
}

// Digest binds the complete review snapshot; it contains no lease capability.
func (p Plan) Digest() string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func openRegular(path string) (*os.File, error) {
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	f := os.NewFile(uintptr(fd), path)
	s, e := f.Stat()
	if e != nil || !s.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("expected regular metadata file")
	}
	return f, nil
}
func readFixed(path string) ([]byte, error) {
	f, e := openRegular(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	raw, e := io.ReadAll(io.LimitReader(f, 8193))
	if len(raw) > 8192 {
		return nil, errors.New("Git metadata exceeds bound")
	}
	return raw, e
}
