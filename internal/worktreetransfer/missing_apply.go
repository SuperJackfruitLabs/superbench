package worktreetransfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

func (m MissingManager) refPrefix(p MissingPlan) string {
	return "refs/superbench/quarantine/" + p.Spec.ID + "/" + filepath.Base(p.Admin) + "/"
}
func (m MissingManager) refs(ctx context.Context, p MissingPlan, complete bool) error {
	expected := map[string]string{}
	for _, object := range p.Objects {
		expected[m.refPrefix(p)+object.OID] = object.OID
	}
	out, err := m.git(ctx, p.Spec.Common, "", "for-each-ref", "--format=%(refname) %(objectname)", m.refPrefix(p))
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || expected[fields[0]] != fields[1] {
			return errors.New("unknown or changed quarantine object ref")
		}
		delete(expected, fields[0])
	}
	if complete && len(expected) > 0 {
		return errors.New("quarantine object retention is incomplete")
	}
	return nil
}
func (m MissingManager) guard(ctx context.Context, p MissingPlan) error {
	if m.Gate == nil {
		return errors.New("missing transfer requires fenced approval gate")
	}
	if err := m.Gate(ctx, p); err != nil {
		return err
	}
	return m.anchors(ctx, p)
}
func (m MissingManager) validPlan(p MissingPlan) error {
	if p.Version != MissingVersion || !missingID.MatchString(p.Spec.ID) || !oid.MatchString(p.Spec.Commit) || !strings.HasPrefix(p.Spec.TaskRef, "refs/heads/") || filepath.Dir(p.Admin) != filepath.Join(p.Spec.Common, "worktrees") || p.Evidence != filepath.Join(p.Spec.Common, "superbench-quarantine", p.Spec.ID+"-"+filepath.Base(p.Admin)+".json") || p.AdminEntries == 0 || p.AdminEntries > missingEntriesLimit || p.AdminBytes > missingAdminLimit || len(p.Objects) == 0 || len(p.Objects) > missingObjectsLimit {
		return errors.New("invalid missing-worktree approval")
	}
	for i, o := range p.Objects {
		if !oid.MatchString(o.OID) || i > 0 && p.Objects[i-1].OID >= o.OID {
			return errors.New("invalid retained object vector")
		}
		switch o.Kind {
		case "commit", "tree", "blob", "tag":
		default:
			return errors.New("invalid retained object kind")
		}
	}
	return nil
}
func (m MissingManager) loadEvidence(ctx context.Context, p MissingPlan) (*missingEvidence, error) {
	raw, err := readBounded(p.Evidence, 16<<20)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(p.Evidence)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, errors.New("quarantine approval is not private owned evidence")
	}
	var e missingEvidence
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&e); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(e)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, errors.New("quarantine evidence approval changed")
	}
	root := filepath.Dir(p.Evidence)
	if err = privateDir(root); err != nil {
		return nil, err
	}
	if e.Version != MissingVersion || e.PlanDigest != p.Digest() || filepath.Base(e.Stage) != e.Stage || !strings.HasPrefix(e.Stage, "data-"+p.Spec.ID+"-") || !reflect.DeepEqual(e.Objects, p.Objects) {
		return nil, errors.New("quarantine evidence belongs to another approval")
	}
	stage := filepath.Join(root, e.Stage)
	if err = privateDir(stage); err != nil {
		return nil, err
	}
	id, err := identity(stage)
	if err != nil || id != e.StageIdentity {
		return nil, errors.New("quarantine evidence container replaced")
	}
	manifest, err := os.Lstat(filepath.Join(stage, "manifest.json"))
	if err != nil || !os.SameFile(info, manifest) {
		return nil, errors.New("quarantine evidence publication identity changed")
	}
	items, err := os.ReadDir(stage)
	if err != nil {
		return nil, err
	}
	if len(items) != 3 || items[0].Name() != "admin" || items[1].Name() != "lock" || items[2].Name() != "manifest.json" {
		return nil, errors.New("unknown quarantine evidence entries")
	}
	full, protected, size, count := adminSummary(e.Entries)
	if full != p.AdminDigest || protected != p.ProtectedDigest || size != p.AdminBytes || count != p.AdminEntries {
		return nil, errors.New("quarantine evidence snapshot changed")
	}
	copies, err := adminSnapshot(ctx, filepath.Join(stage, "admin"))
	if err != nil || !reflect.DeepEqual(copies, e.Copies) {
		return nil, errors.Join(err, errors.New("retained admin evidence replaced or changed"))
	}
	if len(copies) != len(e.Entries) {
		return nil, errors.New("incomplete retained admin evidence")
	}
	for i, c := range copies {
		original := e.Entries[i]
		if c.Path != original.Path || c.Size != original.Size || c.Digest != original.Digest {
			return nil, errors.New("retained admin bytes differ from approval")
		}
	}
	budget := int64(missingAdminLimit)
	lock, err := snapshot(ctx, filepath.Join(stage, "lock"), "lock", &budget)
	if err != nil || lock != e.Lock {
		return nil, errors.New("owned quarantine lock evidence changed")
	}
	lockBytes, err := readFixed(filepath.Join(stage, "lock"))
	if err != nil || string(lockBytes) != MissingVersion+"\n"+p.Digest()+"\n" {
		return nil, errors.New("owned quarantine lock content changed")
	}
	return &e, nil
}
func (m MissingManager) ownedLock(p MissingPlan, e *missingEvidence, required bool) error {
	path := filepath.Join(p.Admin, "locked")
	s, err := os.Lstat(path)
	if os.IsNotExist(err) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	if e == nil {
		return errors.New("preexisting worktree lock is not owned")
	}
	owned, err := os.Lstat(filepath.Join(filepath.Dir(p.Evidence), e.Stage, "lock"))
	if err != nil || !os.SameFile(s, owned) {
		return errors.New("unknown or replaced worktree lock")
	}
	return nil
}
func (m MissingManager) current(ctx context.Context, p MissingPlan, e *missingEvidence, retained bool) error {
	if err := evidenceRoot(p); err != nil {
		return err
	}
	if err := m.noPromisor(ctx, p); err != nil {
		return err
	}
	if err := m.anchors(ctx, p); err != nil {
		return err
	}
	admin, err := m.findAdmin(ctx, p.Spec, retained)
	if err != nil || admin != p.Admin {
		return errors.New("missing registration changed")
	}
	if err = m.head(ctx, p, e != nil, !retained); err != nil {
		return err
	}
	budget := int64(missingAdminLimit)
	config, err := snapshot(ctx, filepath.Join(p.Spec.Common, "config"), "config", &budget)
	if err != nil || config != p.Config {
		return errors.New("mirror configuration changed")
	}
	entries, err := adminSnapshot(ctx, p.Admin)
	if err != nil {
		return err
	}
	full, protected, _, _ := adminSummary(entries)
	if e == nil {
		if full != p.AdminDigest {
			return errors.New("missing administration changed after review")
		}
		if err = missingUnlocked(p.Admin); err != nil {
			return err
		}
		out, e2 := m.git(ctx, p.Spec.Common, "", "for-each-ref", "--format=%(refname)", m.refPrefix(p))
		if e2 != nil {
			return e2
		}
		if strings.TrimSpace(out) != "" {
			return errors.New("quarantine refs have no published ownership evidence")
		}
		return nil
	}
	if protected != p.ProtectedDigest {
		return errors.New("protected missing administration changed")
	}
	if err = m.ownedLock(p, e, false); err != nil {
		return err
	}
	// The only permitted reflog mutation is this approval's same-commit detach.
	original := ""
	found := false
	for _, entry := range e.Entries {
		if entry.Path == "logs/HEAD" {
			raw, e2 := readBounded(filepath.Join(filepath.Dir(p.Evidence), e.Stage, "admin", "logs/HEAD"), missingAdminLimit)
			if e2 != nil {
				return e2
			}
			original = string(raw)
			found = true
		}
	}
	actual, err := readBounded(filepath.Join(p.Admin, "logs/HEAD"), missingAdminLimit)
	if os.IsNotExist(err) && !found {
		actual = nil
		err = nil
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(actual), original) {
		return errors.New("original HEAD reflog changed")
	}
	suffix := strings.TrimPrefix(string(actual), original)
	if suffix != "" {
		if !strings.HasSuffix(suffix, "\tsuperbench missing quarantine "+p.Spec.ID+"\n") || strings.Count(suffix, "\n") != 1 || !strings.HasPrefix(suffix, p.Spec.Commit+" "+p.Spec.Commit+" ") {
			return errors.New("unexpected quarantine HEAD reflog mutation")
		}
	}
	return m.refs(ctx, p, false)
}

// Preflight is read-only and does not require mutation authority. A caller must
// preflight every present/missing member while holding its whole-vector lock.
func (m MissingManager) Preflight(ctx context.Context, p MissingPlan) error {
	c, cancel, err := m.bounded(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if err = m.validPlan(p); err != nil {
		return err
	}
	e, err := m.loadEvidence(c, p)
	if err != nil {
		return err
	}
	return m.current(c, p, e, false)
}

// Apply retains evidence and local object roots, then locks and detaches the
// exact absent checkout registration. It never creates or removes checkout files.
func (m MissingManager) Apply(ctx context.Context, p MissingPlan) error {
	c, cancel, err := m.bounded(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if err = m.validPlan(p); err != nil {
		return err
	}
	if err = m.guard(c, p); err != nil {
		return err
	}
	_, _, _, roots, err := openMissingAnchors(p.Spec.Checkout)
	if err != nil {
		return err
	}
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	for _, path := range []string{p.Spec.Common, p.Admin} {
		root, e2 := os.OpenRoot(path)
		if e2 != nil {
			return e2
		}
		roots = append(roots, root)
		info, e2 := root.Lstat(".")
		if e2 != nil {
			return e2
		}
		id, e2 := infoIdentity(info)
		want := p.CommonIdentity
		if path == p.Admin {
			want = p.AdminIdentity
		}
		if e2 != nil || id != want {
			return errors.New("approved administration descriptor changed")
		}
	}
	e, err := m.loadEvidence(c, p)
	if err != nil {
		return err
	}
	if err = m.current(c, p, e, false); err != nil {
		return err
	}
	if e == nil {
		entries, err := adminSnapshot(c, p.Admin)
		if err != nil {
			return err
		}
		retained, err := m.publishEvidence(c, p, entries)
		if err != nil {
			return err
		}
		e = &retained
		if err = m.guard(c, p); err != nil {
			return err
		}
	}
	for _, object := range p.Objects {
		if err = m.guard(c, p); err != nil {
			return err
		}
		if err = m.current(c, p, e, false); err != nil {
			return err
		}
		ref := m.refPrefix(p) + object.OID
		out, e2 := m.git(c, p.Spec.Common, "", "for-each-ref", "--format=%(objectname)", ref)
		if e2 != nil {
			return e2
		}
		if strings.TrimSpace(out) == "" {
			_, e2 = m.git(c, p.Spec.Common, "", "update-ref", ref, object.OID, strings.Repeat("0", len(object.OID)))
		} else if strings.TrimSpace(out) != object.OID {
			return errors.New("quarantine object ref changed")
		}
		if e2 = errors.Join(e2, m.guard(c, p)); e2 != nil {
			return e2
		}
	}
	if err = m.refs(c, p, true); err != nil {
		return err
	}
	if err = m.guard(c, p); err != nil {
		return err
	}
	if err = m.current(c, p, e, false); err != nil {
		return err
	}
	if _, err = os.Lstat(filepath.Join(p.Admin, "locked")); os.IsNotExist(err) {
		err = os.Link(filepath.Join(filepath.Dir(p.Evidence), e.Stage, "lock"), filepath.Join(p.Admin, "locked"))
		if err == nil {
			err = syncDir(p.Admin)
		}
	}
	if err = errors.Join(err, m.guard(c, p)); err != nil {
		return err
	}
	if err = m.ownedLock(p, e, true); err != nil {
		return err
	}
	if err = m.current(c, p, e, false); err != nil {
		return err
	}
	if err = m.guard(c, p); err != nil {
		return err
	}
	head, err := readFixed(filepath.Join(p.Admin, "HEAD"))
	if err != nil {
		return err
	}
	if string(head) != p.Spec.Commit+"\n" {
		_, err = m.git(c, p.Spec.Common, p.Admin, "update-ref", "--no-deref", "-m", "superbench missing quarantine "+p.Spec.ID, "HEAD", p.Spec.Commit, p.Spec.Commit)
	}
	if err = errors.Join(err, m.guard(c, p)); err != nil {
		return err
	}
	if err = m.current(c, p, e, false); err != nil {
		return err
	}
	if err = m.refs(c, p, true); err != nil {
		return err
	}
	return m.ownedLock(p, e, true)
}

// VerifyRetained proves completed quarantine without requiring the original task
// tip to remain unchanged. It grants no new ownership or mutation authority.
func (m MissingManager) VerifyRetained(ctx context.Context, p MissingPlan) error {
	c, cancel, err := m.bounded(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if err = m.validPlan(p); err != nil {
		return err
	}
	e, err := m.loadEvidence(c, p)
	if err != nil {
		return err
	}
	if e == nil {
		return errors.New("quarantine evidence is missing")
	}
	if err = m.current(c, p, e, true); err != nil {
		return err
	}
	head, err := readFixed(filepath.Join(p.Admin, "HEAD"))
	if err != nil || string(head) != p.Spec.Commit+"\n" {
		return errors.New("quarantine HEAD is not exactly detached")
	}
	if err = m.ownedLock(p, e, true); err != nil {
		return err
	}
	return m.refs(c, p, true)
}
