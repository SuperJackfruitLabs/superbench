// Package quotaoperationcontrol binds the indexed journal to installed storage
// policy. It exposes no readiness callbacks, first-install grant or caller paths.
package quotaoperationcontrol

import (
	"context"
	"errors"
	"os"
	"reflect"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
)

const PolicyPath = "/etc/superbench/storage.json"

var ErrPolicy = errors.New("installed indexed operation policy refused")

// Open reads existing policy, allocation authority and indexed history. Neither
// a missing database nor an empty allocation ledger authorizes initialization.
func Open(ctx context.Context) (*quotaoperationjournal.Control, hoststorage.Policy, error) {
	var zero hoststorage.Policy
	if err := ctx.Err(); err != nil {
		return nil, zero, err
	}
	if os.Getuid() != 0 || os.Geteuid() != 0 {
		return nil, zero, ErrPolicy
	}
	info, err := os.Lstat(PolicyPath)
	if err != nil {
		return nil, zero, err
	}
	if !policyFile(info) {
		return nil, zero, ErrPolicy
	}
	p, err := hoststorage.Load(PolicyPath)
	if err != nil {
		return nil, zero, err
	}
	if p.OperationJournal != "indexed-v2" {
		return nil, zero, ErrPolicy
	}
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if os.Getuid() != 0 || os.Geteuid() != 0 {
			return ErrPolicy
		}
		now, err := os.Lstat(PolicyPath)
		if err != nil {
			return err
		}
		if !policyFile(now) || !os.SameFile(info, now) || info.Mode() != now.Mode() {
			return ErrPolicy
		}
		actual, err := hoststorage.Load(PolicyPath)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, p) {
			return ErrPolicy
		}
		end, err := os.Lstat(PolicyPath)
		if err != nil || !policyFile(end) || !os.SameFile(info, end) || end.Mode() != info.Mode() {
			return ErrPolicy
		}
		return ctx.Err()
	}
	c, err := quotaoperationjournal.OpenExisting(ctx, quotaoperationjournal.Config{
		Directory: p.Ledger, CheckControl: check,
		CheckReady: func(ctx context.Context) (string, error) {
			if err := check(ctx); err != nil {
				return "", err
			}
			authority, err := p.OperationAuthority(ctx)
			if err != nil {
				return "", err
			}
			if err := check(ctx); err != nil {
				return "", err
			}
			return authority, nil
		},
	})
	if err != nil {
		return nil, zero, err
	}
	return c, p, nil
}

func policyFile(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && st.Uid == 0 && st.Nlink == 1 && info.Mode().Perm()&0022 == 0 && info.Size() > 0 && info.Size() <= 64<<10
}
