package workspacepolicy

import (
	"errors"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func validateRecovery(b *model.Bench) error {
	source := b.Workspace.RecoveredFrom
	if source == nil {
		return nil
	}
	invalid := errors.New("invalid aggregate recovery provenance")
	if !identity.MatchString(source.BenchID) || source.BenchID == b.Workspace.ID || !digestPattern.MatchString(source.ManifestSHA256) || !model.ValidCommit(source.Commit) || strings.ToLower(source.Commit) != source.Commit {
		return invalid
	}
	found := false
	for _, m := range b.Workspace.Members {
		if m.Repository != source.Repository {
			continue
		}
		// The source seal is a commit in its metadata repository. Its width follows
		// that allowlisted repository, even if another member coordinates new seals.
		if m.ObjectFormat == "sha1" && len(source.Commit) != 40 || m.ObjectFormat == "sha256" && len(source.Commit) != 64 {
			return invalid
		}
		found = true
	}
	if !found {
		return invalid
	}
	return nil
}
