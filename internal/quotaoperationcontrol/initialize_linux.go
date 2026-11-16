//go:build linux

package quotaoperationcontrol

import (
	"context"
	"os"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperationjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagebootstrap"
)

// InitializeFresh is direct-root first-install only. It never stops, masks or
// configures a service, adopts old allocation records, or treats absent indexed
// history as permission to recreate it. Failure evidence is always retained.
func InitializeFresh(ctx context.Context) (*quotaoperationjournal.Control, hoststorage.Policy, error) {
	var zero hoststorage.Policy
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		return nil, zero, ErrInstall
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, zero, err
	}
	pin, p, err := installedPolicy()
	if err != nil {
		return nil, zero, err
	}
	defer pin.close()
	pinned := pin.check
	if err = installationGate(ctx, p); err != nil {
		return nil, zero, err
	}
	fresh, err := storagebootstrap.Inspect(ctx, p)
	if err != nil {
		return nil, zero, err
	}
	defer fresh.Close()
	check := func(ctx context.Context) error {
		if err := pinned(ctx); err != nil {
			return err
		}
		if err := installationGate(ctx, p); err != nil {
			return err
		}
		return fresh.Check(ctx)
	}
	init, err := quotaoperationjournal.NewInitializer(quotaoperationjournal.FreshConfig{
		Control: quotaoperationjournal.Config{Directory: p.Ledger, CheckControl: check,
			CheckReady: func(ctx context.Context) (string, error) {
				if err := check(ctx); err != nil {
					return "", err
				}
				authority, err := p.OperationAuthority(ctx)
				if err != nil {
					return "", err
				}
				if err = check(ctx); err != nil {
					return "", err
				}
				return authority, nil
			}},
		Authorize: func(ctx context.Context) error {
			if err := check(ctx); err != nil {
				return err
			}
			return fresh.Empty()
		},
	})
	if err != nil {
		return nil, zero, err
	}
	_, err = init.InitializeFresh(ctx, func(ctx context.Context, ledger quotaoperationjournal.FreshLedger) error {
		if err := check(ctx); err != nil {
			return err
		}
		if err := fresh.Initialize(ctx, ledger); err != nil {
			return err
		}
		return check(ctx)
	})
	if err != nil {
		return nil, zero, err
	}
	// The returned handle retains the fresh observation only until this call
	// returns. Reopen from independently verified, now-complete installed records.
	return Open(ctx)
}
