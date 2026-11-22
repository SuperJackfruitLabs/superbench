package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerenew"
)

func TestRenewAdminIsLocalRootOnlyAndTouchesNoStateOtherwise(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the refusal needs a non-root caller")
	}
	root := t.TempDir()
	state := filepath.Join(root, "state")
	for _, op := range []string{"renew_admin", "renew-admin"} {
		var out, errs bytes.Buffer
		err := workspaceCLIContext(context.Background(), []string{op, "--state", state, "--data", filepath.Join(root, "data"), "card"}, &out, &errs)
		if err != workspacerenew.ErrRoot || out.Len() != 0 {
			t.Fatal(op, err, out.String())
		}
		if _, err := os.Stat(state); !os.IsNotExist(err) {
			t.Fatal("refused renewal opened state", err)
		}
		if err := workspaceCLIContext(context.Background(), []string{op, "--holder", "writer", "card"}, &out, &errs); err == nil || err == workspacerenew.ErrRoot {
			t.Fatal("renewal accepted a holder flag", err)
		}
	}
}
