package worktreetransfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// VerifyQuarantine observes only retained Git administration and its private
// evidence. It grants no checkout absence, source fencing, storage, allocation,
// launch, detach or deletion authority. A reviewed archive/reap caller must
// independently bind the current mirror storage and its approved root
// observation while excluding writers. In particular this does not consult the
// original checkout parents or original bench allocation, which an approved
// reap may have removed or retired. Ordinary transfer must use VerifyRetained.
// No evidence is created, adopted, repaired or cleaned up by this method.
func (m MissingManager) VerifyQuarantine(ctx context.Context, p MissingPlan) error {
	c, cancel, err := (Manager{Git: m.Git, Timeout: m.Timeout}).bounded(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if err = c.Err(); err != nil {
		return err
	}
	if m.Git == nil {
		return errors.New("quarantine observation requires managed Git")
	}
	if err = m.validPlan(p); err != nil {
		return err
	}
	// Keep opened physical containers alive throughout the observation. Checking
	// both descriptor and pathname identities again refuses replacement during a
	// slow object inspection without consulting any former checkout parent.
	var roots []*os.Root
	defer func() {
		for _, root := range roots {
			root.Close()
		}
	}()
	for _, path := range []string{p.Spec.Common, p.Admin} {
		if err = physical(path); err != nil {
			return err
		}
		root, e := os.OpenRoot(path)
		if e != nil {
			return e
		}
		roots = append(roots, root)
	}
	if err = quarantineContainers(p, roots); err != nil {
		return err
	}
	evidence, err := m.quarantineEvidence(c, p)
	if err != nil {
		return err
	}
	if err = m.noPromisor(c, p); err != nil {
		return err
	}
	if err = m.refs(c, p, true); err != nil {
		return err
	}
	for _, object := range p.Objects {
		kind, e := m.git(c, p.Spec.Common, "", "cat-file", "-t", object.OID)
		if e != nil {
			return e
		}
		if strings.TrimSpace(kind) != object.Kind {
			return errors.New("retained quarantine object missing or changed kind")
		}
	}
	// Recheck fixed evidence/admin identities after managed Git callbacks; these
	// callbacks must not turn a stale first observation into a current proof.
	after, err := m.quarantineEvidence(c, p)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(evidence, after) {
		return errors.New("quarantine evidence changed during observation")
	}
	if err = m.refs(c, p, true); err != nil {
		return err
	}
	if err = quarantineContainers(p, roots); err != nil {
		return err
	}
	return c.Err()
}

func quarantineContainers(p MissingPlan, roots []*os.Root) error {
	for i, path := range []string{p.Spec.Common, p.Admin} {
		want := p.CommonIdentity
		if i == 1 {
			want = p.AdminIdentity
		}
		if err := physical(path); err != nil {
			return err
		}
		actual, err := identity(path)
		if err != nil {
			return err
		}
		info, err := roots[i].Lstat(".")
		if err != nil {
			return err
		}
		opened, err := infoIdentity(info)
		if err != nil {
			return err
		}
		if !info.IsDir() || actual != want || opened != want {
			return errors.New("retained quarantine container replaced")
		}
	}
	return nil
}

func (m MissingManager) quarantineEvidence(ctx context.Context, p MissingPlan) (*missingEvidence, error) {
	if err := evidenceRoot(p); err != nil {
		return nil, err
	}
	e, err := m.loadEvidence(ctx, p)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, errors.New("quarantine evidence is missing")
	}
	admin, err := m.findAdmin(ctx, p.Spec, true)
	if err != nil {
		return nil, err
	}
	if admin != p.Admin {
		return nil, errors.New("retained quarantine registration changed")
	}
	budget := int64(missingAdminLimit)
	config, err := snapshot(ctx, filepath.Join(p.Spec.Common, "config"), "config", &budget)
	if err != nil {
		return nil, err
	}
	if config != p.Config {
		return nil, errors.New("retained mirror configuration changed")
	}
	entries, err := adminSnapshot(ctx, p.Admin)
	if err != nil {
		return nil, err
	}
	_, protected, _, _ := adminSummary(entries)
	if protected != p.ProtectedDigest {
		return nil, errors.New("protected retained administration changed")
	}
	head, err := readFixed(filepath.Join(p.Admin, "HEAD"))
	if err != nil {
		return nil, err
	}
	if string(head) != p.Spec.Commit+"\n" {
		return nil, errors.New("quarantine HEAD is not exactly detached")
	}
	if err = m.ownedLock(p, e, true); err != nil {
		return nil, err
	}
	if err = quarantineReflog(p, e); err != nil {
		return nil, err
	}
	return e, nil
}

func quarantineReflog(p MissingPlan, e *missingEvidence) error {
	original := []byte(nil)
	for _, entry := range e.Entries {
		if entry.Path == "logs/HEAD" {
			raw, err := readBounded(filepath.Join(filepath.Dir(p.Evidence), e.Stage, "admin", "logs/HEAD"), missingAdminLimit)
			if err != nil {
				return err
			}
			original = raw
		}
	}
	actual, err := readBounded(filepath.Join(p.Admin, "logs/HEAD"), missingAdminLimit)
	if os.IsNotExist(err) && original == nil {
		actual = nil
		err = nil
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(actual), string(original)) {
		return errors.New("original quarantine HEAD reflog changed")
	}
	suffix := strings.TrimPrefix(string(actual), string(original))
	if suffix != "" && (!strings.HasSuffix(suffix, "\tsuperbench missing quarantine "+p.Spec.ID+"\n") || strings.Count(suffix, "\n") != 1 || !strings.HasPrefix(suffix, p.Spec.Commit+" "+p.Spec.Commit+" ")) {
		return errors.New("unexpected retained HEAD reflog mutation")
	}
	return nil
}
