//go:build linux

package quotaoperationcontrol

import (
	"context"
	"os"
	"reflect"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

// CheckMaintenance checks the actual installed policy and stopped, persistently
// masked fixed service, plus the original-host process inventory. It changes no
// configuration and accepts no caller assertion of quiescence. Callers must
// hold operations.lock and recheck before and after their maintenance effects.
func CheckMaintenance(ctx context.Context, expected hoststorage.Policy) error {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		return ErrInstall
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pin, actual, err := installedPolicy()
	if err != nil {
		return err
	}
	defer pin.close()
	if !reflect.DeepEqual(actual, expected) {
		return ErrInstall
	}
	if err = installationGate(ctx, actual); err != nil {
		return err
	}
	return pin.check(ctx)
}
