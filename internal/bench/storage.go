package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

type HostConfig struct {
	DataRoot          string
	QuotaHelper       string
	PolicyOverride    string `json:",omitempty"` // direct-root test/admin only; sudo helper rejects overrides
	MinDataFreeBytes  uint64
	MinStateFreeBytes uint64
	MaxLogBytes       uint64
	MaxLogsBytes      uint64
	MaxLogFiles       int
}
type Space struct{ Free, Total, Device uint64 }
type HostStatus struct {
	Managed     bool
	Data, State Space
	Config      *HostConfig `json:",omitempty"`
}

func (c HostConfig) defaults() HostConfig {
	if c.MaxLogBytes == 0 {
		c.MaxLogBytes = 1 << 20
	}
	if c.MaxLogsBytes == 0 {
		c.MaxLogsBytes = 64 << 20
	}
	if c.MaxLogFiles == 0 {
		c.MaxLogFiles = 4096
	}
	return c
}
func (c HostConfig) validate(data string) error {
	if c.DataRoot != data || !filepath.IsAbs(c.DataRoot) || filepath.Clean(c.DataRoot) != c.DataRoot {
		return errors.New("host config data root differs from the requested data root")
	}
	if !filepath.IsAbs(c.QuotaHelper) || filepath.Clean(c.QuotaHelper) != c.QuotaHelper {
		return errors.New("quota helper must be an absolute normalized path")
	}
	if c.MaxLogBytes != 0 && (c.MaxLogBytes < 1024 || c.MaxLogBytes > 1<<30) {
		return errors.New("per-run log budget must be between 1 KiB and 1 GiB")
	}
	if c.MaxLogsBytes != 0 && (c.MaxLogsBytes < 1<<20 || c.MaxLogsBytes > 1<<40) {
		return errors.New("total log budget must be between 1 MiB and 1 TiB")
	}
	if c.MaxLogFiles < 0 || c.MaxLogFiles > 1000000 {
		return errors.New("invalid log file budget")
	}
	per, total := c.MaxLogBytes, c.MaxLogsBytes
	if per == 0 {
		per = 1 << 20
	}
	if total == 0 {
		total = 64 << 20
	}
	if per > total {
		return errors.New("per-run log budget exceeds total budget")
	}
	if c.MinDataFreeBytes < 1024 || c.MinStateFreeBytes < 1024 {
		return errors.New("host admission reserves must be at least 1 KiB")
	}
	if c.PolicyOverride != "" && (!filepath.IsAbs(c.PolicyOverride) || os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "") {
		return errors.New("quota policy override requires direct root administration")
	}
	return nil
}
func (s Store) hostConfig() (*HostConfig, error) {
	raw, e := os.ReadFile(filepath.Join(s.State, "host.json"))
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var c HostConfig
	if e = json.Unmarshal(raw, &c); e != nil {
		return nil, e
	}
	c = c.defaults()
	return &c, c.validate(s.Data)
}
func (s Store) space(path string) (Space, error) {
	if s.spaceProbe != nil {
		return s.spaceProbe(path)
	}
	for {
		stat, e := os.Stat(path)
		if e == nil {
			var fs syscall.Statfs_t
			if e = syscall.Statfs(path, &fs); e != nil {
				return Space{}, e
			}
			return Space{Free: fs.Bavail * uint64(fs.Bsize), Total: fs.Blocks * uint64(fs.Bsize), Device: uint64(stat.Sys().(*syscall.Stat_t).Dev)}, nil
		}
		if !os.IsNotExist(e) {
			return Space{}, e
		}
		parent := filepath.Dir(path)
		if parent == path {
			return Space{}, e
		}
		path = parent
	}
}
func (s Store) HostStatus() (HostStatus, error) {
	if e := s.validate(); e != nil {
		return HostStatus{}, e
	}
	cfg, e := s.hostConfig()
	if e != nil {
		return HostStatus{}, e
	}
	data, e := s.space(s.Data)
	if e != nil {
		return HostStatus{}, e
	}
	state, e := s.space(s.State)
	return HostStatus{Managed: cfg != nil, Data: data, State: state, Config: cfg}, e
}
func (s Store) admit() error {
	status, e := s.HostStatus()
	if e != nil {
		return e
	}
	minData, minState := uint64(64<<20), uint64(1<<20)
	if status.Config != nil {
		minData = status.Config.MinDataFreeBytes
		minState = status.Config.MinStateFreeBytes
		if status.Data.Device == status.State.Device {
			return errors.New("managed state must be outside the quota data filesystem")
		}
	}
	if status.Data.Free < minData {
		return fmt.Errorf("data free-space reserve reached: %d available, %d required", status.Data.Free, minData)
	}
	if status.State.Free < minState {
		return fmt.Errorf("state free-space reserve reached: %d available, %d required", status.State.Free, minState)
	}
	if status.Config != nil {
		for _, key := range []string{"store", "mirrors"} {
			if _, e = s.quota(*status.Config, "shared", key); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s Store) quota(c HostConfig, kind, key string) (hoststorage.Assignment, error) {
	if s.quotaApply != nil {
		return s.quotaApply(c, kind, key)
	}
	args := []string{}
	if c.PolicyOverride != "" {
		args = append(args, "--policy", c.PolicyOverride)
	}
	args = append(args, "apply", kind, key)
	command := c.QuotaHelper
	if os.Geteuid() != 0 {
		args = append([]string{"-n", command}, args...)
		command = "/usr/bin/sudo"
	}
	cmd := exec.Command(command, args...)
	raw, e := cmd.CombinedOutput()
	if e != nil {
		return hoststorage.Assignment{}, fmt.Errorf("quota allocation: %w: %s", e, string(raw))
	}
	var a hoststorage.Assignment
	if e = json.Unmarshal(raw, &a); e != nil {
		return a, e
	}
	expected := filepath.Join(s.Data, key)
	if kind == "bench" {
		expected = filepath.Join(s.Data, "benches", key)
	}
	if a.Path != expected || a.ProjectID == 0 || a.Bytes == 0 || a.Inodes == 0 {
		return a, errors.New("quota helper returned a conflicting assignment")
	}
	return a, nil
}
func (s Store) ConfigureHost(c HostConfig) error {
	c = c.defaults()
	if e := c.validate(s.Data); e != nil {
		return e
	}
	return s.transaction(func(db database) error {
		prior, e := s.hostConfig()
		if e != nil {
			return e
		}
		if prior != nil && *prior != c {
			return errors.New("host is already configured; explicit migration required")
		}
		if prior == nil && len(db) != 0 {
			return errors.New("managed host requires fresh state; existing benches are not silently adopted")
		}
		data, e := s.space(s.Data)
		if e != nil {
			return e
		}
		state, e := s.space(s.State)
		if e != nil {
			return e
		}
		if data.Device == state.Device {
			return errors.New("managed state must be outside the quota data filesystem")
		}
		if data.Free < c.MinDataFreeBytes || state.Free < c.MinStateFreeBytes {
			return errors.New("insufficient host reserve for configuration")
		}
		for _, key := range []string{"store", "mirrors"} {
			if _, e = s.quota(c, "shared", key); e != nil {
				return e
			}
		}
		raw, e := json.MarshalIndent(c, "", "  ")
		if e != nil {
			return e
		}
		return atomicWrite(s.State, filepath.Join(s.State, "host.json"), raw)
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
	a, e := s.quota(*config, "bench", c.Bench.Key)
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
	a, e := s.quota(*c, "bench", b.Key)
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
	a, e := s.quota(*c, "bench", b.Key)
	if e != nil {
		return e
	}
	if a.ProjectID != b.ProjectID || a.Bytes != b.QuotaBytes || a.Inodes == 0 {
		return errors.New("state upgrade differs from the original allocation")
	}
	b.QuotaInodes = a.Inodes
	return nil
}
