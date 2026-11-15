package hoststorage

import (
	"context"
	"encoding/json"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"os"
	"path/filepath"
	"syscall"
)

type Worker struct {
	Context     context.Context
	State, Data string
	Probe       func(string) (Space, error)
	Apply       func(HostConfig, string, string, uint64) (Assignment, error)
}
type HostConfig = hostpolicy.Config

type Space struct{ Free, Total, Device uint64 }
type HostStatus struct {
	Managed     bool
	Data, State Space
	Config      *HostConfig `json:",omitempty"`
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
	return quotahelper.Apply(s.Context, quotahelper.Config{State: s.State, Data: s.Data, Command: c.QuotaHelper, PolicyOverride: c.PolicyOverride}, kind, key, requested)
}
