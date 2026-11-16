//go:build linux

package payloadgit

import (
	"context"
	"errors"
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
func TestGitInputAndOutputBoundsRefuseWithoutPublication(t *testing.T) {
	for _, test := range []struct {
		args              []string
		in                []byte
		inLimit, outLimit uint64
	}{{nil, nil, 0, 1024}, {[]string{"version"}, []byte("too large"), 1, 1024}, {[]string{"version"}, nil, 0, 0}} {
		out, err := (Route{}).Execute(context.Background(), gitcontrol.Runner{}, test.args, test.in, test.inLimit, test.outLimit)
		if !errors.Is(err, ErrRefused) || out != nil {
			t.Fatalf("invalid bounds reached Git: %q %v", out, err)
		}
	}
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
