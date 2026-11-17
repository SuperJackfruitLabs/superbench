//go:build linux

package quotaoperationcontrol

import (
	"context"
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

func TestMaintenanceGateRefusesSudoBeforeInstalledAuthority(t *testing.T) {
	t.Setenv("SUDO_UID", "123")
	if err := CheckMaintenance(context.Background(), hoststorage.Policy{}); !errors.Is(err, ErrInstall) {
		t.Fatal(err)
	}
}
