package setup

import (
	"context"
	"errors"
)

// QuotaAt reads only a bounded regular config blob at the immutable source.
// Invalid/unreadable config is left for normal strict setup validation after
// registration, preserving the initialization lease on configuration failure.
func QuotaAt(mirror, source string) (uint64, error) {
	return QuotaAtContext(context.Background(), mirror, source)
}

func QuotaAtContext(ctx context.Context, mirror, source string) (uint64, error) {
	raw, e := configBlob(ctx, mirror, source)
	if errors.Is(e, errConfigBlob) {
		return 0, nil
	}
	if e != nil {
		return 0, e
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
