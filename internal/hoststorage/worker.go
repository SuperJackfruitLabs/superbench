package hoststorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

type Worker struct {
	State, Data string
	Probe       func(string) (Space, error)
	Apply       func(HostConfig, string, string, uint64) (Assignment, error)
}
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

func (c HostConfig) Defaults() HostConfig {
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
func (c HostConfig) Validate(data string) error {
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
func (s Worker) Config() (*HostConfig, error) {
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
	c = c.Defaults()
	return &c, c.Validate(s.Data)
}
func (s Worker) Space(path string) (Space, error) {
	if s.Probe != nil {
		return s.Probe(path)
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
func (s Worker) Status() (HostStatus, error) {
	cfg, e := s.Config()
	if e != nil {
		return HostStatus{}, e
	}
	data, e := s.Space(s.Data)
	if e != nil {
		return HostStatus{}, e
	}
	state, e := s.Space(s.State)
	return HostStatus{Managed: cfg != nil, Data: data, State: state, Config: cfg}, e
}
func (s Worker) Quota(c HostConfig, kind, key string, requested uint64) (Assignment, error) {
	if s.Apply != nil {
		return s.Apply(c, kind, key, requested)
	}
	args := []string{}
	if c.PolicyOverride != "" {
		args = append(args, "--policy", c.PolicyOverride)
	}
	args = append(args, "apply", kind, key)
	if requested != 0 {
		args = append(args, fmt.Sprint(requested))
	}
	command := c.QuotaHelper
	if os.Geteuid() != 0 {
		args = append([]string{"-n", command}, args...)
		command = "/usr/bin/sudo"
	}
	cmd := exec.Command(command, args...)
	raw, e := cmd.CombinedOutput()
	if e != nil {
		return Assignment{}, fmt.Errorf("quota allocation: %w: %s", e, string(raw))
	}
	var a Assignment
	if e = json.Unmarshal(raw, &a); e != nil {
		return a, e
	}
	expected := filepath.Join(s.Data, key)
	if kind == "bench" {
		expected = filepath.Join(s.Data, "benches", key)
	}
	if a.Path != expected || a.ProjectID == 0 || a.Bytes == 0 || a.Inodes == 0 || requested != 0 && a.Bytes != requested {
		return a, errors.New("quota helper returned a conflicting assignment")
	}
	return a, nil
}
