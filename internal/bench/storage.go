package bench

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

type HostConfig = hoststorage.HostConfig
type CapacityError = hoststorage.CapacityError
type Space = hoststorage.Space
type HostStatus = hoststorage.HostStatus

func (s Store) workerStorage() hoststorage.Worker {
	return hoststorage.Worker{Context: s.controls, State: s.State, Data: s.Data, Probe: s.spaceProbe, Apply: s.quotaApply}
}
func (s Store) hostConfig() (*HostConfig, error) { return s.workerStorage().Config() }
func (s Store) space(path string) (Space, error) { return s.workerStorage().Space(path) }
func (s Store) HostStatus() (HostStatus, error) {
	if e := s.validate(); e != nil {
		return HostStatus{}, e
	}
	return s.workerStorage().Status()
}
func (s Store) admit() error {
	if err := s.validate(); err != nil {
		return err
	}
	return s.workerStorage().Admit()
}
func (s Store) ConfigureHost(c HostConfig) error {
	c = c.Defaults()
	if err := c.Validate(s.Data); err != nil {
		return err
	}
	return s.transaction(func(db database) error {
		return s.workerStorage().Configure(c, len(db))
	})
}
func (s Store) applyBenchQuota(c *creation) error {
	config, e := s.hostConfig()
	if e != nil {
		return e
	}
	if config == nil {
		return nil
	}
	a, e := s.workerStorage().Quota(*config, "bench", c.Bench.Key, c.Bench.RequestedQuotaBytes)
	if e != nil {
		return e
	}
	if c.Bench.ProjectID != 0 && (c.Bench.ProjectID != a.ProjectID || c.Bench.QuotaBytes != a.Bytes || c.Bench.QuotaInodes != 0 && c.Bench.QuotaInodes != a.Inodes) {
		return errors.New("creation intent conflicts with quota allocation")
	}
	c.Bench.ProjectID = a.ProjectID
	c.Bench.QuotaBytes = a.Bytes
	c.Bench.QuotaInodes = a.Inodes
	return s.record(*c)
}

func (s Store) releaseQuota(b *Bench) error { return s.workerStorage().ReleaseBenchQuota(b) }

func (s Store) ensureBenchQuota(b *Bench) error { return s.workerStorage().EnsureBenchQuota(b) }

func (s Store) pinMigratedInodes(b *Bench) error {
	if b.ProjectID == 0 || b.QuotaInodes != 0 || b.ReapedAt != "" || b.ReapingDigest != "" {
		return nil
	}
	c, e := s.hostConfig()
	if e != nil {
		return e
	}
	if c == nil {
		return errors.New("managed state upgrade requires its host policy")
	}
	a, e := s.workerStorage().Quota(*c, "bench", b.Key, b.RequestedQuotaBytes)
	if e != nil {
		return e
	}
	if a.ProjectID != b.ProjectID || a.Bytes != b.QuotaBytes || a.Inodes == 0 {
		return errors.New("state upgrade differs from the original allocation")
	}
	b.QuotaInodes = a.Inodes
	return nil
}
