// Package workspacepolicy validates durable aggregate identities without effects.
package workspacepolicy

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

var identity = regexp.MustCompile(`^[0-9a-f]{32}$`)
var token = regexp.MustCompile(`^([0-9a-f]{32}|[0-9a-f]{48})$`)
var key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

func shortBranch(s string) bool {
	return utf8.ValidString(s) && len(s) <= 255 && s != "HEAD" && s != "@" && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "refs/") && model.ValidBranchRef("refs/heads/"+s)
}

// Validate accepts only initialization records; readiness requires a later,
// explicit lifecycle implementation rather than an inferred clean checkout.
func Validate(b *model.Bench) error {
	invalid := errors.New("invalid aggregate workspace record")
	if b == nil || b.Workspace == nil || !key.MatchString(b.Key) {
		return invalid
	}
	w := b.Workspace
	if !identity.MatchString(w.ID) || !key.MatchString(w.CreationID) || (w.Phase != "materializing" && w.Phase != "initializing") {
		return invalid
	}
	if b.Repo != "" || b.Branch != "" || b.Mirror != "" || b.Checkpoint != "" || b.CreatedFrom != "" || b.ArchivedAt != "" || b.ReapAfter != "" || b.ArchiveFreezeDigest != "" || b.ArchiveFrozenAt != "" || b.ArchiveMerge != nil || b.ArchiveUnmerged != nil || b.ReapingDigest != "" || b.ReapedAt != "" {
		return invalid
	}
	l := b.Lease
	if l == nil || l.Holder != "initialization" || !token.MatchString(l.Token) || !identity.MatchString(l.Continuation) || l.Execution != nil || len(l.RetainedRuns) != 0 || l.StopRequested {
		return invalid
	}
	if !filepath.IsAbs(b.Path) || filepath.Clean(b.Path) != b.Path {
		return invalid
	}
	data := filepath.Dir(filepath.Dir(b.Path))
	if b.Path != filepath.Join(data, "benches", b.Key) || !quotapolicy.ValidBytes(b.RequestedQuotaBytes) || !quotapolicy.ValidBytes(w.ExplicitQuotaBytes) || w.ExplicitQuotaBytes != 0 && w.ExplicitQuotaBytes != b.RequestedQuotaBytes {
		return invalid
	}
	if b.ProjectID == 0 {
		if b.QuotaBytes != 0 || b.QuotaInodes != 0 {
			return invalid
		}
	} else if b.QuotaBytes == 0 || !quotapolicy.ValidBytes(b.QuotaBytes) || b.QuotaInodes == 0 || b.QuotaInodes > 1<<40 || b.RequestedQuotaBytes != 0 && b.RequestedQuotaBytes != b.QuotaBytes {
		return invalid
	}
	manifest := checkpointset.Manifest{Version: checkpointset.Version, BenchID: w.ID, GenerationID: strings.Repeat("0", 32), CheckpointRepository: w.CheckpointRepository}
	for _, m := range w.Members {
		if !shortBranch(m.Branch) || !shortBranch(m.Base) || m.Path != filepath.Join(b.Path, m.Name) || m.Mirror != repository.Path(data, m.Repository) || !quotapolicy.ValidBytes(m.RequestedQuotaBytes) || m.RequestedQuotaBytes != 0 && m.RequestedQuotaBytes != b.RequestedQuotaBytes {
			return invalid
		}
		pin, err := checkpointset.PinRef(w.ID, manifest.GenerationID, m.Name)
		if err != nil {
			return invalid
		}
		manifest.Members = append(manifest.Members, checkpointset.Member{Name: m.Name, Repository: m.Repository, TaskRef: "refs/heads/" + m.Branch, PinRef: pin, ObjectFormat: m.ObjectFormat, Commit: m.Source})
	}
	if err := manifest.Validate(); err != nil {
		return invalid
	}
	return nil
}
