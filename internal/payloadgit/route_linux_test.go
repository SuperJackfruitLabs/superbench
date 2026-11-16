//go:build linux

package payloadgit

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
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

func TestLayoutMatchingHandlesScalarWithoutGroupedMemberIndex(t *testing.T) {
	scalar := Route{scalar: true}
	scalarLayout := payloadexec.Layout{Scalar: true, Members: []payloadexec.LayoutMember{{Name: ""}}}
	if !scalar.matchesLayout(scalarLayout) {
		t.Fatal("scalar route refused its one unnamed registered member")
	}
	grouped := Route{member: "api", members: []model.WorkspaceMember{{Name: "api"}, {Name: "web"}}}
	groupedLayout := payloadexec.Layout{Members: []payloadexec.LayoutMember{{Name: "api"}, {Name: "web"}}}
	if !grouped.matchesLayout(groupedLayout) {
		t.Fatal("exact grouped vector refused")
	}
	for name, tc := range map[string]struct {
		r Route
		l payloadexec.Layout
	}{
		"scalar-empty":      {scalar, payloadexec.Layout{Scalar: true}},
		"scalar-named":      {scalar, payloadexec.Layout{Scalar: true, Members: []payloadexec.LayoutMember{{Name: "api"}}}},
		"scalar-extra":      {scalar, payloadexec.Layout{Scalar: true, Members: []payloadexec.LayoutMember{{}, {}}}},
		"scalar-as-grouped": {scalar, groupedLayout},
		"grouped-as-scalar": {grouped, scalarLayout},
		"grouped-missing":   {grouped, payloadexec.Layout{Members: []payloadexec.LayoutMember{{Name: "api"}}}},
		"grouped-reordered": {grouped, payloadexec.Layout{Members: []payloadexec.LayoutMember{{Name: "web"}, {Name: "api"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.r.matchesLayout(tc.l) {
				t.Fatal("mismatched registration accepted")
			}
		})
	}
}
