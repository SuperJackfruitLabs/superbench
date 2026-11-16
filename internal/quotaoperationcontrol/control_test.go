package quotaoperationcontrol

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestOpenRequiresRootBeforeAnyFixedPolicyRead(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("ordinary checks require non-root")
	}
	c, p, err := Open(context.Background())
	if !errors.Is(err, ErrPolicy) || c != nil || p.Root != "" {
		t.Fatalf("unprivileged indexed authority: %v %+v %v", c, p, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, p, err = Open(ctx)
	if !errors.Is(err, context.Canceled) || c != nil || p.Root != "" {
		t.Fatalf("canceled authority: %v %+v %v", c, p, err)
	}
}

func TestInstalledPolicyMetadataRejectsNonRegularAndUntrustedFiles(t *testing.T) {
	if policyFile(nil) {
		t.Fatal("nil policy accepted")
	}
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil || policyFile(info) {
		t.Fatal("directory policy accepted", err)
	}
	path := dir + "/policy.json"
	if err = os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || policyFile(info) {
		t.Fatal("non-root-owned policy accepted", err)
	}
}
