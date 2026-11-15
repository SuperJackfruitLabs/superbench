// Package hostpolicy validates persisted worker configuration independently
// from privileged quota allocation and storage mutation.
package hostpolicy

import (
	"errors"
	"os"
	"path/filepath"
)

type Config struct {
	DataRoot          string
	QuotaHelper       string
	PolicyOverride    string `json:",omitempty"` // direct-root test/admin only; sudo helper rejects overrides
	MinDataFreeBytes  uint64
	MinStateFreeBytes uint64
	MaxLogBytes       uint64
	MaxLogsBytes      uint64
	MaxLogFiles       int
}

func (c Config) Defaults() Config {
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
func (c Config) Validate(data string) error {
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
