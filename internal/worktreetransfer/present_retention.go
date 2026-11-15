package worktreetransfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// PresentRetention is explicit administrative evidence retention, never missing
// checkout, task-tip, allocation, detach or deletion authority. Its distinct
// wrapper prevents a present checkout from being passed off as an absent one.
type PresentRetention struct {
	Version string
	Plan    MissingPlan
}

const PresentRetentionVersion = "superbench.present-administration.v1"

func (m MissingManager) InspectPresentRetention(ctx context.Context, s MissingSpec) (PresentRetention, error) {
	var result PresentRetention
	c, cancel, e := (Manager{Git: m.Git, Timeout: m.Timeout}).bounded(ctx)
	if e != nil {
		return result, e
	}
	defer cancel()
	if e = c.Err(); e != nil {
		return result, e
	}
	p := MissingPlan{Version: MissingVersion, Spec: s}
	if !missingID.MatchString(s.ID) || !oid.MatchString(s.Commit) || !strings.HasPrefix(s.TaskRef, "refs/heads/") {
		return result, errors.New("invalid present administration identity")
	}
	for _, path := range []string{s.Checkout, s.Common} {
		if e = physical(path); e != nil {
			return result, e
		}
	}
	common, e := m.git(c, s.Checkout, "", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil || strings.TrimSpace(common) != s.Common {
		return result, errors.New("present administration common directory changed")
	}
	admin, e := m.git(c, s.Checkout, "", "rev-parse", "--absolute-git-dir")
	if e != nil {
		return result, e
	}
	p.Admin = strings.TrimSpace(admin)
	if filepath.Dir(p.Admin) != filepath.Join(s.Common, "worktrees") {
		return result, errors.New("present administration is not a linked worktree")
	}
	if e = physical(p.Admin); e != nil {
		return result, e
	}
	p.CommonIdentity, e = identity(s.Common)
	if e != nil {
		return result, e
	}
	p.AdminIdentity, e = identity(p.Admin)
	if e != nil {
		return result, e
	}
	if e = missingUnlocked(p.Admin); e != nil {
		return result, e
	}
	head, e := readFixed(filepath.Join(p.Admin, "HEAD"))
	if e != nil || string(head) != s.Commit+"\n" {
		return result, errors.New("present administration must be detached at the sealed commit")
	}
	if e = m.head(c, p, true, false); e != nil {
		return result, e
	}
	budget := int64(missingAdminLimit)
	p.Config, e = snapshot(c, filepath.Join(s.Common, "config"), "config", &budget)
	if e != nil {
		return result, e
	}
	entries, e := adminSnapshot(c, p.Admin)
	if e != nil {
		return result, e
	}
	p.AdminDigest, p.ProtectedDigest, p.AdminBytes, p.AdminEntries = adminSummary(entries)
	p.Objects, e = m.objects(c, p, entries)
	if e != nil {
		return result, e
	}
	p.Evidence = filepath.Join(s.Common, "superbench-quarantine", s.ID+"-"+filepath.Base(p.Admin)+".json")
	if e = evidenceRoot(p); e != nil {
		return result, e
	}
	if _, e = os.Lstat(p.Evidence); !os.IsNotExist(e) {
		return result, errors.New("present administration evidence already exists")
	}
	if e = m.refs(c, p, false); e != nil {
		return result, e
	}
	result = PresentRetention{Version: PresentRetentionVersion, Plan: p}
	return result, m.presentCurrent(c, result, false)
}
func (m MissingManager) presentCurrent(ctx context.Context, r PresentRetention, complete bool) error {
	p := r.Plan
	if r.Version != PresentRetentionVersion {
		return errors.New("invalid present retention version")
	}
	if e := m.validPlan(p); e != nil {
		return e
	}
	for path, want := range map[string]Identity{p.Spec.Common: p.CommonIdentity, p.Admin: p.AdminIdentity} {
		if e := physical(path); e != nil {
			return e
		}
		got, e := identity(path)
		if e != nil || got != want {
			return errors.New("present administration container changed")
		}
	}
	budget := int64(missingAdminLimit)
	config, e := snapshot(ctx, filepath.Join(p.Spec.Common, "config"), "config", &budget)
	if e != nil || config != p.Config {
		return errors.New("retained present configuration changed")
	}
	entries, e := adminSnapshot(ctx, p.Admin)
	if e != nil {
		return e
	}
	full, protected, _, _ := adminSummary(entries)
	evidence, e := m.loadEvidence(ctx, p)
	if e != nil {
		return e
	}
	if evidence == nil {
		if complete || full != p.AdminDigest {
			return errors.New("present administration changed before retention")
		}
		return missingUnlocked(p.Admin)
	}
	if protected != p.ProtectedDigest {
		return errors.New("protected present administration changed")
	}
	if e = m.head(ctx, p, true, false); e != nil {
		return e
	}
	raw, e := readFixed(filepath.Join(p.Admin, "HEAD"))
	if e != nil || string(raw) != p.Spec.Commit+"\n" {
		return errors.New("retained present HEAD changed")
	}
	if e = quarantineReflog(p, evidence); e != nil {
		return e
	}
	if e = m.ownedLock(p, evidence, complete); e != nil {
		return e
	}
	if e = m.refs(ctx, p, complete); e != nil {
		return e
	}
	return ctx.Err()
}

// VerifyPresentRetention has no former-checkout/storage dependency. complete
// must be true for deletion/retired-state use; false permits only publication
// retries while the caller still holds its independently stopped source.
func (m MissingManager) VerifyPresentRetention(ctx context.Context, r PresentRetention, complete bool) error {
	c, cancel, e := (Manager{Git: m.Git, Timeout: m.Timeout}).bounded(ctx)
	if e != nil {
		return e
	}
	defer cancel()
	if e = c.Err(); e != nil {
		return e
	}
	if e = m.presentCurrent(c, r, complete); e != nil {
		return e
	}
	if complete {
		return m.VerifyQuarantine(c, r.Plan)
	}
	return m.presentCurrent(c, r, false)
}

// RetainPresentAdministration copies exact admin bytes and pins all unknown
// index/reflog objects before locking the detached registration. No checkout
// byte, task ref, HEAD or reflog is changed, and no evidence is deleted.
func (m MissingManager) RetainPresentAdministration(ctx context.Context, r PresentRetention) error {
	c, cancel, e := (Manager{Git: m.Git, Timeout: m.Timeout}).bounded(ctx)
	if e != nil {
		return e
	}
	defer cancel()
	if e = c.Err(); e != nil {
		return e
	}
	p := r.Plan
	gate := func() error {
		if m.Gate == nil {
			return errors.New("present retention requires stopped source gate")
		}
		if e := m.Gate(c, p); e != nil {
			return e
		}
		return m.presentCurrent(c, r, false)
	}
	if e = gate(); e != nil {
		return e
	}
	evidence, e := m.loadEvidence(c, p)
	if e != nil {
		return e
	}
	if evidence == nil {
		entries, e := adminSnapshot(c, p.Admin)
		if e != nil {
			return e
		}
		copy, e := m.publishEvidenceWithGate(c, p, entries, func(context.Context, MissingPlan) error { return gate() })
		if e != nil {
			return e
		}
		evidence = &copy
	}
	for _, object := range p.Objects {
		if e = gate(); e != nil {
			return e
		}
		ref := m.refPrefix(p) + object.OID
		actual, e := m.git(c, p.Spec.Common, "", "for-each-ref", "--format=%(objectname)", ref)
		if e != nil {
			return e
		}
		if strings.TrimSpace(actual) == "" {
			_, e = m.git(c, p.Spec.Common, "", "update-ref", ref, object.OID, strings.Repeat("0", len(object.OID)))
		} else if strings.TrimSpace(actual) != object.OID {
			return errors.New("present retention object root changed")
		}
		if e != nil {
			return e
		}
	}
	if e = gate(); e != nil {
		return e
	}
	if _, e = os.Lstat(filepath.Join(p.Admin, "locked")); os.IsNotExist(e) {
		e = os.Link(filepath.Join(filepath.Dir(p.Evidence), evidence.Stage, "lock"), filepath.Join(p.Admin, "locked"))
		if e == nil {
			e = syncDir(p.Admin)
		}
	}
	if e != nil {
		return e
	}
	if e = gate(); e != nil {
		return e
	}
	before, e := m.loadEvidence(c, p)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(before, evidence) {
		return errors.New("present retention publication changed")
	}
	return m.VerifyPresentRetention(c, r, true)
}
