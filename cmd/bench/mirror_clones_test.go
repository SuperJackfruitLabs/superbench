package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/mirrorstage"
)

func TestMirrorCloneCLIRejectsNonlocalOrInvalidInputBeforeStateEffects(t *testing.T) {
	for _, tail := range [][]string{
		{"inspect", "--socket=", "0123456789abcdef0123456789abcdef"},
		{"inspect", "--token=", "0123456789abcdef0123456789abcdef"},
		{"inspect", "--approve=anything", "0123456789abcdef0123456789abcdef"},
		{"collect", "--plan=", "0123456789abcdef0123456789abcdef"},
		{"inspect", "not-a-clone-id"},
	} {
		t.Run(strings.Join(tail, " "), func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			args := append([]string{tail[0], "--state", state, "--data", filepath.Join(root, "data")}, tail[1:]...)
			var out, diag bytes.Buffer
			if e := mirrorClonesContext(context.Background(), args, &out, &diag); e == nil {
				t.Fatal("invalid input accepted")
			}
			if _, e := os.Lstat(state); !os.IsNotExist(e) {
				t.Fatal("invalid input touched state", e)
			}
			if out.Len() != 0 {
				t.Fatal("invalid input reported a result", out.String())
			}
		})
	}
}

func TestMirrorCloneCLIReportsControlFailureWithoutDeletingApprovedData(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	data := filepath.Join(root, "data")
	parent := filepath.Join(data, "mirrors")
	for _, path := range []string{state, parent} {
		if e := os.MkdirAll(path, 0700); e != nil {
			t.Fatal(e)
		}
	}
	p, e := mirrorstage.CreateManaged(state, parent, filepath.Join(parent, strings.Repeat("a", 64)+".git"), filepath.Join(root, "not-a-cgroup"))
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(p.Stage, "unpublished")
	if e = os.WriteFile(file, []byte("preserve"), 0600); e != nil {
		t.Fatal(e)
	}
	// The component fixture has no writer. The actual CLI must still independently
	// reject its fake control configuration rather than trust structural validity.
	m := mirrorstage.Manager{State: state, Parent: parent, Within: func(fn func() error) error { return fn() }, VerifyControl: func(context.Context, string, string) error { return nil }}
	plan, e := m.Inspect(context.Background(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := plan.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "plan.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	var out, diag bytes.Buffer
	e = mirrorClonesContext(context.Background(), []string{"collect", "--state", state, "--data", data, "--plan", path, "--approve", plan.Digest, p.ID}, &out, &diag)
	if e == nil {
		t.Fatal("unavailable managed control reported success", out.String())
	}
	if out.Len() != 0 {
		t.Fatal("control failure emitted successful result", out.String())
	}
	got, e := os.ReadFile(file)
	if e != nil || string(got) != "preserve" {
		t.Fatal("failed control validation changed data", string(got), e)
	}
}
