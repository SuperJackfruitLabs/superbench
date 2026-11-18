// Package hoststoragetest tests host storage through its exported API,
// outside the package, which is at its size bound.
package hoststoragetest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestCancelledOperationCannotLaunchPrivilegedQuotaHelper(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "quota-helper")
	marker := filepath.Join(root, "started")
	// The executable is owned by this fixture; no sudo or real quota call occurs.
	script := "#!/bin/sh\nprintf started > '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("durable cancellation")
	i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "quota", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(func() error) error { return denied })
	if err != nil {
		t.Fatal(err)
	}
	// Direct root helper is not required; a denied Start must precede even sudo.
	w := hoststorage.Worker{State: root, Data: filepath.Join(root, "data"), Context: ctx}
	if _, err = w.Quota(hoststorage.HostConfig{QuotaHelper: helper}, "bench", "card", 0); !errors.Is(err, denied) {
		t.Fatal("helper launch crossed cancel", err)
	}
	if _, err = os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled helper created files", err)
	}
}
