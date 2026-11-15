package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

type HostConfig = hoststorage.HostConfig
type Space = hoststorage.Space
type HostStatus = hoststorage.HostStatus

func (s Store) workerStorage() hoststorage.Worker {
	return hoststorage.Worker{State: s.State, Data: s.Data, Probe: s.spaceProbe, Apply: s.quotaApply}
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

func (s Store) releaseQuota(b *Bench) error {
	c, e := s.hostConfig()
	if e != nil {
		return e
	}
	if c == nil {
		return errors.New("managed bench has no host policy")
	}
	args := []string{}
	if c.PolicyOverride != "" {
		args = append(args, "--policy", c.PolicyOverride)
	}
	args = append(args, "release", "bench", b.Key, fmt.Sprint(b.ProjectID))
	command := c.QuotaHelper
	if os.Geteuid() != 0 {
		args = append([]string{"-n", command}, args...)
		command = "/usr/bin/sudo"
	}
	raw, e := exec.Command(command, args...).CombinedOutput()
	if e != nil {
		return fmt.Errorf("quota release: %w: %s", e, raw)
	}
	var a hoststorage.Assignment
	if e = json.Unmarshal(raw, &a); e != nil {
		return e
	}
	if a.Path != b.Path || a.ProjectID != b.ProjectID || a.Bytes != 0 || a.Inodes != 0 {
		return errors.New("quota helper did not confirm retired assignment")
	}
	return nil
}

func (s Store) ensureBenchQuota(b *Bench) error {
	c, e := s.hostConfig()
	if e != nil {
		return e
	}
	if c == nil {
		if b.ProjectID != 0 {
			return errors.New("managed bench has no host policy")
		}
		return nil
	}
	a, e := s.workerStorage().Quota(*c, "bench", b.Key, b.RequestedQuotaBytes)
	if e != nil {
		return e
	}
	if a.ProjectID != b.ProjectID || a.Bytes != b.QuotaBytes || a.Inodes == 0 || a.Inodes != b.QuotaInodes {
		return errors.New("bench quota differs from its pinned byte/inode allocation; explicit migration required")
	}
	return nil
}
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
