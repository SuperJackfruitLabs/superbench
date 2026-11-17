package hoststorage

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// AssignWorkspace applies one pinned aggregate allocation without changing its policy.
func (w Worker) AssignWorkspace(b *model.Bench) error {
	c, err := w.Config()
	if err != nil {
		return err
	}
	if c == nil {
		if b.ProjectID != 0 {
			return errors.New("managed workspace has no host policy")
		}
		return nil
	}
	a, err := w.Quota(*c, "bench", b.Key, b.RequestedQuotaBytes)
	if err != nil {
		return err
	}
	if a.Path != b.Path || a.ProjectID == 0 || a.Bytes == 0 || a.Inodes == 0 || b.RequestedQuotaBytes != 0 && a.Bytes != b.RequestedQuotaBytes || b.ProjectID != 0 && (b.ProjectID != a.ProjectID || b.QuotaBytes != a.Bytes || b.QuotaInodes != a.Inodes) {
		return errors.New("workspace quota differs from its pinned allocation")
	}
	b.ProjectID, b.QuotaBytes, b.QuotaInodes = a.ProjectID, a.Bytes, a.Inodes
	return nil
}

// EnsureBenchQuota independently verifies the exact recorded allocation.
func (w Worker) EnsureBenchQuota(b *model.Bench) error {
	c, e := w.Config()
	if e != nil {
		return e
	}
	if c == nil {
		if b.ProjectID != 0 {
			return errors.New("managed bench has no host policy")
		}
		return nil
	}
	a, e := w.Quota(*c, "bench", b.Key, b.RequestedQuotaBytes)
	if e != nil {
		return e
	}
	if a.ProjectID != b.ProjectID || a.Bytes != b.QuotaBytes || a.Inodes == 0 || a.Inodes != b.QuotaInodes {
		return errors.New("bench quota differs from its pinned byte/inode allocation; explicit migration required")
	}
	return nil
}

// VerifyFreshQuota checks a Fresh workspace's pinned allocation read-only. The
// helper refuses a legacy apply for it, so the daemon never reapplies it.
func (w Worker) VerifyFreshQuota(b *model.Bench) error { return w.verifyFreshQuota(b, storageCommand) }
func (w Worker) verifyFreshQuota(b *model.Bench, command func(context.Context, string, ...string) ([]byte, error)) error {
	if w.Context == nil || b.ProjectID < 10000 || b.QuotaBytes == 0 || b.QuotaInodes == 0 {
		return errors.New("fresh quota verification requires a context and a pinned allocation")
	}
	_, err := w.inspectStorage(w.Context, b.Key, Assignment{Path: b.Path, ProjectID: b.ProjectID, Bytes: b.QuotaBytes, Inodes: b.QuotaInodes}, command)
	return err
}
