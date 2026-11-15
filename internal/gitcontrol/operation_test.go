package gitcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestOperationBarrierPreventsEvenUnmanagedGitLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	denied := errors.New("durable cancellation barrier")
	i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "launch", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(func() error) error { return denied })
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "touch \"$1\"", "sh", marker)
	if err = Run(ctx, cmd); !errors.Is(err, denied) {
		t.Fatal("launch crossed cancellation", err)
	}
	if _, err = os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("canceled command created files", err)
	}
}

func TestControlJournalPinsOperationEpochAndPreservesLegacyRecovery(t *testing.T) {
	state := canonicalTemp(t)
	path, j := fixtureIntent(t, state, state)
	j.OperationID = strings.Repeat("c", 64)
	j.OperationEpoch = strings.Repeat("d", 32)
	write := func() {
		raw, e := json.Marshal(j)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	write()
	got, err := readControl(filepath.Dir(path), filepath.Base(path))
	if err != nil || got != j {
		t.Fatal("operation provenance lost", got, err)
	}
	j.OperationEpoch = ""
	write()
	if _, err = readControl(filepath.Dir(path), filepath.Base(path)); err == nil {
		t.Fatal("partial operation provenance accepted")
	}
	j.Version = "superbench.git-control.v1"
	j.OperationEpoch = strings.Repeat("d", 32)
	write()
	if _, err = readControl(filepath.Dir(path), filepath.Base(path)); err == nil {
		t.Fatal("future authority adopted by legacy intent")
	}
	j.OperationID = ""
	j.OperationEpoch = ""
	write()
	if _, err = readControl(filepath.Dir(path), filepath.Base(path)); err != nil {
		t.Fatal("legitimate legacy journal refused", err)
	}
}
