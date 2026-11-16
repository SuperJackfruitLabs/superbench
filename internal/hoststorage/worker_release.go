package hoststorage

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

func (s Worker) ReleaseBenchQuota(b *model.Bench) error {
	c, e := s.Config()
	if e != nil {
		return e
	}
	if c == nil {
		return errors.New("managed bench has no host policy")
	}
	a, e := quotahelper.Release(quotahelper.Config{State: s.State, Data: s.Data, Command: c.QuotaHelper, PolicyOverride: c.PolicyOverride}, b.Key, b.ProjectID)
	if e != nil {
		return e
	}
	if a.Path != b.Path || a.ProjectID != b.ProjectID || a.Bytes != 0 || a.Inodes != 0 {
		return errors.New("quota helper did not confirm retired assignment")
	}
	return nil
}
