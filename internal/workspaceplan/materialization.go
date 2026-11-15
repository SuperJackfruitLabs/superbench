package workspaceplan

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"path/filepath"
	"strings"
)

func ValidateMaterialization(plan Plan) (string, error) {
	spec := Spec{Key: plan.Key, CheckpointRepository: plan.CheckpointRepository, QuotaBytes: plan.QuotaBytes}
	for _, member := range plan.Members {
		spec.Repositories = append(spec.Repositories, Repository{Name: member.Name, Repository: member.Repository, Branch: member.Branch, Base: member.Base})
	}
	normalized, err := Normalize(spec)
	if err != nil {
		return "", err
	}
	data := filepath.Dir(filepath.Dir(plan.Root))
	if !filepath.IsAbs(plan.Root) || filepath.Clean(plan.Root) != plan.Root || plan.Root != filepath.Join(data, "benches", plan.Key) {
		return "", errors.New("workspace root differs from its registered location")
	}
	for i, member := range plan.Members {
		r := normalized.Repositories[i]
		if member.Branch != r.Branch || member.Base != r.Base || member.Path != filepath.Join(plan.Root, member.Name) || member.Mirror != repository.Path(data, member.Repository) || !model.ValidCommit(member.Source) || !quotapolicy.ValidBytes(member.RepositoryQuotaBytes) || member.RepositoryQuotaBytes != 0 && member.RepositoryQuotaBytes != plan.QuotaBytes {
			return "", errors.New("workspace member differs from the pinned normalized plan")
		}
		if member.ObjectFormat != "sha1" && member.ObjectFormat != "sha256" || member.ObjectFormat == "sha1" && len(member.Source) != 40 || member.ObjectFormat == "sha256" && len(member.Source) != 64 {
			return "", errors.New("invalid workspace member object format")
		}
		for _, previous := range plan.Members[:i] {
			if previous.Repository == member.Repository && BranchNamespaceConflict(previous.Branch, member.Branch) {
				return "", errors.New("workspace task branches conflict in the repository ref namespace")
			}
		}
	}
	return data, nil
}

func BranchNamespaceConflict(a, b string) bool {
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
