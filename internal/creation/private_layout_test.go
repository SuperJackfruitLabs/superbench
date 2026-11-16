package creation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func TestPrivateLayoutNeverUsesLegacyMaterialization(t *testing.T) {
	data := t.TempDir()
	root := filepath.Join(data, "benches", "card")
	f := Files{Data: data, State: t.TempDir(), Git: func(string, ...string) (string, error) { t.Fatal("legacy Git accessed private layout"); return "", nil }}
	f.Write = func(_, p string, b []byte) error { return os.WriteFile(p, b, 0600) }
	c := Intent{Version: 1, Source: "sealed", Base: "main", Bench: model.Bench{Key: "card", Repo: "repo", Branch: "task", RegistrationID: "0123456789abcdef0123456789abcdef", PayloadLayoutVersion: payloadpolicy.LayoutVersion, AllocationRoot: root, Path: filepath.Join(root, "worktree")}}
	// Journal identity exists before private filesystem bootstrap; this API must
	// not interpret absent paths as authority to create or adopt a worktree.
	if err := f.Record(&c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Finish(&c); !errors.Is(err, model.ErrScalarLayout) {
		t.Fatal("wrong refusal", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("legacy materialization changed allocation", err)
	}
	c.Bench.AllocationRoot = filepath.Join(data, "other", "benches", "card")
	if err := f.Record(&c); err == nil {
		t.Fatal("creation retry redirected allocation")
	}
}
