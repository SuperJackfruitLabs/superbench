//go:build linux

package hostidentity_test

import (
	"context"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hostidentity"
)

func TestNativeLinuxHandoffAndManagedExecutionObserveSameHost(t *testing.T) {
	actual, err := hostidentity.Current(context.Background())
	if err != nil {
		t.Fatal("native host observation unavailable", err)
	}
	managed, err := execution.CurrentIdentity()
	if err != nil || actual != managed.HostID {
		t.Fatal("handoff and managed execution disagree on their actual host", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hostidentity.Current(ctx); err == nil {
		t.Fatal("cancelled observation returned an identity")
	}
}
