package worktreetransfer

import (
	"bytes"
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
	"unicode/utf8"
)

const MissingVersion = "superbench.missing-worktree.v1"
const missingAdminLimit = 64 << 20
const missingEntriesLimit = 100000
const missingObjectsLimit = 4096

// MissingManager requires independent storage identity and fenced-source
// authority. Neither missing files nor a Git lock establishes writer fencing.
type MissingManager struct {
	Git           func(context.Context, string, ...string) (string, error)
	Gate          func(context.Context, MissingPlan) error
	VerifyStorage func(context.Context, json.RawMessage) error
	Timeout       time.Duration
}
type MissingSpec struct {
	ID, Checkout, Common, TaskRef, Commit string
	Storage                               json.RawMessage
}
type Anchor struct {
	Path     string
	Identity Identity
}
type RetainedObject struct{ OID, Kind string }
type MissingPlan struct {
	Version                            string
	Spec                               MissingSpec
	MissingAt, Parent, Admin, Evidence string
	Anchors                            []Anchor
	CommonIdentity, AdminIdentity      Identity
	Config                             Entry
	AdminDigest, ProtectedDigest       string
	AdminBytes                         uint64
	AdminEntries                       uint64
	Objects                            []RetainedObject
}

func (p MissingPlan) Digest() string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

var missingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

func (m MissingManager) bounded(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if m.VerifyStorage == nil || m.Git == nil {
		return nil, nil, errors.New("missing transfer requires managed Git and storage verification")
	}
	return (Manager{Git: m.Git, Timeout: m.Timeout}).bounded(ctx)
}
func (m MissingManager) storage(ctx context.Context, raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > 65536 || !utf8.Valid(raw) || !json.Valid(raw) {
		return errors.New("invalid missing-transfer storage proof")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil || !bytes.Equal(compact.Bytes(), raw) {
		return errors.New("storage proof must retain compact JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("storage proof must be an object")
	}
	if err = storageObject(d); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing storage proof data")
	}
	return m.VerifyStorage(ctx, raw)
}
func (m MissingManager) git(ctx context.Context, common, admin string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	prefix := []string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}
	if admin != "" {
		prefix = append(prefix, "--git-dir="+admin, "--work-tree="+common)
	}
	raw, err := m.Git(ctx, common, append(prefix, args...)...)
	if len(raw) > 8<<20 {
		return "", errors.New("missing-transfer Git output exceeds bound")
	}
	return raw, err
}
func (m MissingManager) Inspect(ctx context.Context, s MissingSpec) (MissingPlan, error) {
	c, cancel, err := m.bounded(ctx)
	if err != nil {
		return MissingPlan{}, err
	}
	defer cancel()
	if !missingID.MatchString(s.ID) || !strings.HasPrefix(s.TaskRef, "refs/heads/") || !oid.MatchString(s.Commit) {
		return MissingPlan{}, errors.New("invalid missing transfer identity")
	}
	if err = m.storage(c, s.Storage); err != nil {
		return MissingPlan{}, err
	}
	p := MissingPlan{Version: MissingVersion, Spec: s}
	if err = physical(s.Common); err != nil {
		return p, err
	}
	p.CommonIdentity, err = identity(s.Common)
	if err != nil {
		return p, err
	}
	anchors, missing, parent, err := missingAnchors(s.Checkout)
	if err != nil {
		return p, err
	}
	p.Anchors, p.MissingAt, p.Parent = anchors, missing, parent
	if _, err = m.git(c, s.Common, "", "check-ref-format", s.TaskRef); err != nil {
		return p, err
	}
	p.Admin, err = m.findAdmin(c, s, false)
	if err != nil {
		return p, err
	}
	p.AdminIdentity, err = identity(p.Admin)
	if err != nil {
		return p, err
	}
	if err = missingUnlocked(p.Admin); err != nil {
		return p, err
	}
	if err = m.head(c, p, false, true); err != nil {
		return p, err
	}
	budget := int64(missingAdminLimit)
	p.Config, err = snapshot(c, filepath.Join(s.Common, "config"), "config", &budget)
	if err != nil {
		return p, err
	}
	if !os.FileMode(p.Config.Identity.Mode).IsRegular() {
		return p, errors.New("common config is not regular")
	}
	entries, err := adminSnapshot(c, p.Admin)
	if err != nil {
		return p, err
	}
	p.AdminDigest, p.ProtectedDigest, p.AdminBytes, p.AdminEntries = adminSummary(entries)
	rawEntries, _ := json.Marshal(entries)
	if len(rawEntries)*3+(1<<20) > 16<<20 {
		return p, errors.New("missing admin evidence manifest bound exceeded")
	}
	p.Objects, err = m.objects(c, p, entries)
	if err != nil {
		return p, err
	}
	p.Evidence = filepath.Join(s.Common, "superbench-quarantine", s.ID+"-"+filepath.Base(p.Admin)+".json")
	if err = evidenceRoot(p); err != nil {
		return p, err
	}
	if _, err = os.Lstat(p.Evidence); !os.IsNotExist(err) {
		return p, errors.New("missing transfer evidence already exists")
	}
	if err = m.refs(c, p, false); err != nil {
		return p, err
	}
	existing, err := m.git(c, s.Common, "", "for-each-ref", "--format=%(refname)", m.refPrefix(p))
	if err != nil || strings.TrimSpace(existing) != "" {
		return p, errors.New("quarantine refs already exist")
	}
	// Recheck both storage and path bindings after every read-only observation.
	err = m.anchors(c, p)
	return p, err
}

// missingAnchors proves exactly one ENOENT leaf below an open physical parent.
// It never collapses EACCES/ENOTDIR or a missing mount into checkout absence.
func missingAnchors(path string) ([]Anchor, string, string, error) {
	anchors, missing, parent, roots, err := openMissingAnchors(path)
	for _, root := range roots {
		root.Close()
	}
	return anchors, missing, parent, err
}
func openMissingAnchors(path string) ([]Anchor, string, string, []*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, "", "", nil, errors.New("missing checkout path must be normalized absolute")
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, "", "", nil, err
	}
	roots := []*os.Root{root}
	fail := func(e error) ([]Anchor, string, string, []*os.Root, error) {
		for _, r := range roots {
			r.Close()
		}
		return nil, "", "", nil, e
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	var anchors []Anchor
	for _, part := range parts {
		info, err := root.Lstat(".")
		if err != nil {
			return fail(err)
		}
		id, err := infoIdentity(info)
		if err != nil {
			return fail(err)
		}
		if !info.IsDir() {
			return fail(errors.New("non-directory parent"))
		}
		anchors = append(anchors, Anchor{current, id})
		nextInfo, err := root.Lstat(part)
		if os.IsNotExist(err) {
			return anchors, filepath.Join(current, part), current, roots, nil
		}
		if err != nil {
			return fail(err)
		}
		if !nextInfo.IsDir() || nextInfo.Mode()&os.ModeSymlink != 0 {
			return fail(errors.New("checkout or ancestor is not a physical directory"))
		}
		child, err := root.OpenRoot(part)
		if err != nil {
			return fail(err)
		}
		roots = append(roots, child)
		opened, err := child.Lstat(".")
		if err != nil || !os.SameFile(nextInfo, opened) {
			return fail(errors.New("missing parent replaced"))
		}
		current = filepath.Join(current, part)
		root = child
	}
	return fail(errors.New("checkout reappeared; preserve it"))
}
func infoIdentity(info os.FileInfo) (Identity, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Identity{}, errors.New("unsupported filesystem identity")
	}
	return Identity{uint64(st.Dev), uint64(st.Ino), uint32(info.Mode())}, nil
}
func storageObject(d *json.Decoder) error {
	seen := map[string]bool{}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return e
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return errors.New("ambiguous storage proof")
		}
		seen[name] = true
		if e = storageValue(d); e != nil {
			return e
		}
	}
	_, e := d.Token()
	return e
}
func storageValue(d *json.Decoder) error {
	token, e := d.Token()
	if e != nil {
		return e
	}
	switch token {
	case json.Delim('{'):
		return storageObject(d)
	case json.Delim('['):
		for d.More() {
			if e = storageValue(d); e != nil {
				return e
			}
		}
		_, e = d.Token()
	}
	return e
}

func (m MissingManager) anchors(ctx context.Context, p MissingPlan) error {
	if err := m.storage(ctx, p.Spec.Storage); err != nil {
		return err
	}
	anchors, missing, parent, err := missingAnchors(p.Spec.Checkout)
	if err != nil {
		return err
	}
	if missing != p.MissingAt || parent != p.Parent || !reflect.DeepEqual(anchors, p.Anchors) {
		return errors.New("missing-checkout physical anchors changed")
	}
	for path, want := range map[string]Identity{p.Spec.Common: p.CommonIdentity, p.Admin: p.AdminIdentity} {
		if err = physical(path); err != nil {
			return err
		}
		actual, e := identity(path)
		if e != nil || actual != want {
			return errors.New("missing-transfer administration replaced")
		}
	}
	return ctx.Err()
}
func missingUnlocked(admin string) error {
	for _, name := range []string{"locked", "HEAD.lock", "index.lock"} {
		if _, err := os.Lstat(filepath.Join(admin, name)); !os.IsNotExist(err) {
			return errors.New("missing worktree is locked or has an active metadata writer")
		}
	}
	return nil
}
func (m MissingManager) findAdmin(ctx context.Context, s MissingSpec, allowOther bool) (string, error) {
	root := filepath.Join(s.Common, "worktrees")
	if err := physical(root); err != nil {
		return "", err
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	if len(dirs) > missingEntriesLimit {
		return "", errors.New("worktree registration bound exceeded")
	}
	selected := ""
	for _, d := range dirs {
		if !d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return "", errors.New("non-directory worktree registration")
		}
		admin := filepath.Join(root, d.Name())
		back, e := readFixed(filepath.Join(admin, "gitdir"))
		if e != nil {
			return "", e
		}
		head, e := readFixed(filepath.Join(admin, "HEAD"))
		if e != nil {
			return "", e
		}
		if string(back) == filepath.Join(s.Checkout, ".git")+"\n" {
			if selected != "" {
				return "", errors.New("duplicate checkout registration")
			}
			selected = admin
		} else if !allowOther && string(head) == "ref: "+s.TaskRef+"\n" {
			return "", errors.New("task ref belongs to another registration")
		}
	}
	if selected == "" {
		return "", errors.New("missing checkout has no exact registration")
	}
	common, err := readFixed(filepath.Join(selected, "commondir"))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(selected, strings.TrimSpace(string(common))))
	if err != nil || resolved != s.Common {
		return "", errors.New("registration common directory changed")
	}
	return selected, nil
}
func (m MissingManager) head(ctx context.Context, p MissingPlan, allowDetached, requireTip bool) error {
	raw, err := readFixed(filepath.Join(p.Admin, "HEAD"))
	if err != nil {
		return err
	}
	if string(raw) != "ref: "+p.Spec.TaskRef+"\n" && (!allowDetached || string(raw) != p.Spec.Commit+"\n") {
		return errors.New("missing worktree HEAD changed")
	}
	refs := []string{"HEAD"}
	if requireTip {
		refs = append(refs, p.Spec.TaskRef)
	}
	for _, ref := range refs {
		out, e := m.git(ctx, p.Spec.Common, p.Admin, "rev-parse", "--verify", ref)
		if e != nil || strings.TrimSpace(out) != p.Spec.Commit {
			return errors.New("missing checkout task tip changed")
		}
	}
	return nil
}
