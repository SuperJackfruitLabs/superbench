package setup

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// QuotaAt reads only a bounded regular config blob at the immutable source.
// Invalid/unreadable config is left for normal strict setup validation after
// registration, preserving the initialization lease on configuration failure.
func QuotaAt(mirror, source string) (uint64, error) {
	listing, e := exec.Command("git", "-C", mirror, "ls-tree", source, "--", ".bench/config.toml").Output()
	if e != nil {
		return 0, e
	}
	fields := strings.Fields(string(listing))
	if len(fields) == 0 {
		return 0, nil
	}
	if len(fields) != 4 {
		return 0, errors.New("invalid configuration tree entry")
	}
	if (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
		return 0, nil
	}
	size, e := exec.Command("git", "-C", mirror, "cat-file", "-s", fields[2]).Output()
	if e != nil {
		return 0, e
	}
	n, e := strconv.ParseUint(strings.TrimSpace(string(size)), 10, 64)
	if e != nil {
		return 0, e
	}
	if n > MaxConfig {
		return 0, nil
	}
	raw, e := exec.Command("git", "-C", mirror, "cat-file", "blob", fields[2]).Output()
	if e != nil {
		return 0, e
	}
	if uint64(len(raw)) != n || len(raw) > MaxConfig {
		return 0, errors.New("configuration blob changed during quota selection")
	}
	return quotaFromConfig(raw), nil
}

func MatchQuota(c *Config, pinned uint64) error {
	var requested uint64
	if c != nil {
		var e error
		requested, e = c.Quota.Bytes()
		if e != nil {
			return e
		}
	}
	if requested != pinned {
		return errors.New("repository quota request changed; preserve this allocation and recover under a new key")
	}
	return nil
}
