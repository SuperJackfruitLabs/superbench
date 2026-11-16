package reapsecurity_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

// The privileged CI fixture explicitly runs this case as root. Ordinary test
// runs cannot claim to have verified the kernel's effective-UID boundary.
func TestRootCannotReapEvenAnOtherwiseValidOwnedTree(t *testing.T) {
	if os.Getenv("SUPERBENCH_ROOT_REAP_TEST") != "1" {
		t.Skip("requires explicit owned privileged fixture")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root refusal fixture must actually have effective UID zero")
	}
	root := t.TempDir()
	path := filepath.Join(root, "owned-file")
	if err := os.WriteFile(path, []byte("preserve even approved bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := reap.Snapshot(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reap.DeleteContents(root, plan); err == nil || !strings.Contains(err.Error(), "reap must not run as root") {
		t.Fatal("root deletion was not specifically refused", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "preserve even approved bytes" {
		t.Fatal("root refusal changed approved bytes", err)
	}
	// Invalid input must not hide whether the privileged entry boundary ran.
	_, err = (bench.Store{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data")}).Reap(bench.ReapPlan{})
	if err == nil || !strings.Contains(err.Error(), "reap must not run as root") {
		t.Fatal("Store.Reap did not refuse root before state/plan access", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("root refusal touched state", err)
	}
}
