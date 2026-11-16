//go:build linux

package payloadgit

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
	"io"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func TestMissingAuthorityCannotFallBackToOrdinaryGit(t *testing.T) {
	if _, err := New(serviceidentity.Authority{}, nil, ""); !errors.Is(err, ErrRefused) {
		t.Fatalf("wrong registration refusal: %v", err)
	}
	data, err := (Route{}).Execute(context.Background(), gitcontrol.Runner{}, []string{"version"}, nil, 0, 1024)
	if !errors.Is(err, ErrRefused) || data != nil {
		t.Fatalf("missing authority ran trusted Git: %q %v", data, err)
	}
}
func TestGitOutputOverflowPreservesAcceptedBytes(t *testing.T) {
	out := &bounded{limit: 3}
	if n, err := out.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("bounded write failed: %d %v", n, err)
	}
	if n, err := out.Write([]byte("d")); n != 0 || !errors.Is(err, io.ErrShortBuffer) || out.String() != "abc" {
		t.Fatalf("overflow published bytes: %q %d %v", out.String(), n, err)
	}
}
func TestGitCommandGuardPrecedesAuthorityRevalidation(t *testing.T) {
	tr := &transport{}
	if err := tr.Check(); !errors.Is(err, ErrRefused) {
		t.Fatalf("zero transport reached authority: %v", err)
	}
}

func TestExecuteRejectsLeadingGitOptionsBeforeAuthority(t *testing.T) {
	for _, args := range [][]string{{"-c", "core.hooksPath=/tmp/hooks", "status"}, {"--git-dir=/tmp/other", "status"}, {"--no-optional-locks", "status"}} {
		out, err := (Route{}).Execute(context.Background(), gitcontrol.Runner{}, args, nil, 0, 1024)
		if out != nil || !errors.Is(err, ErrRefused) || !errors.Is(err, gitpolicy.ErrPolicy) {
			t.Fatalf("leading option reached authority instead of policy refusal: %q %v", out, err)
		}
	}
	_, err := (Route{}).Execute(context.Background(), gitcontrol.Runner{}, []string{"status"}, nil, 0, 1024)
	if !errors.Is(err, ErrRefused) || errors.Is(err, gitpolicy.ErrPolicy) {
		t.Fatalf("valid subcommand did not reach separate authority refusal: %v", err)
	}
}
