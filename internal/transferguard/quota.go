package transferguard

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

// The approval binds the explicit request, which can be unset. Resolve the
// effective budget independently from the sealed configuration, as staging does;
// neither mutable checkout files nor mutually consistent row fields prove it.
func checkQuota(ctx context.Context, c Config, p approval, target *model.Bench) error {
	invalid := errors.New("incoming transfer sealed quota differs")
	members := target.Workspace.Members
	if target.Workspace.ExplicitQuotaBytes != p.QuotaBytes || len(members) == 0 || len(members) != len(p.Repositories) || len(members) != len(p.Checkpoint.Members) {
		return invalid
	}
	resolved := p.QuotaBytes
	for i, m := range members {
		r, cp := p.Repositories[i], p.Checkpoint.Members[i]
		if m.Name != r.Name || m.Name != cp.Name || m.Repository != r.Repository || m.Source != cp.Commit || m.Mirror != repository.Path(c.Data, r.Repository) {
			return invalid
		}
		quota, err := setup.QuotaAtContext(ctx, m.Mirror, cp.Commit)
		if err != nil {
			return err
		}
		if quota != m.RequestedQuotaBytes {
			return invalid
		}
		if quota != 0 {
			if resolved != 0 && resolved != quota {
				return invalid
			}
			resolved = quota
		}
	}
	if target.RequestedQuotaBytes != resolved {
		return invalid
	}
	return nil
}
