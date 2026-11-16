//go:build linux

package payloadexec

import (
	"errors"
	"strings"
	"testing"
)

func TestRefusalNeverExposesPrivateCauseOrUnrecognizedPhase(t *testing.T) {
	private := errors.New("private/path secret-token=fixture")
	for _, phase := range []string{"caller", "policy", "stdio", "descriptors", "frame", "scope", "layout", "boundary", "environment", "executable", "credentials", "exec"} {
		err := &refusalError{phase: phase, cause: errors.Join(ErrBoundary, private)}
		if RefusalCode(err) != phase || !errors.Is(err, private) || !errors.Is(err, ErrBoundary) || err.Error() != "managed payload refused: "+phase {
			t.Fatal("closed refusal lost category or internal cause")
		}
	}
	for _, err := range []error{nil, private, &refusalError{phase: "untrusted/private/path", cause: private}} {
		if RefusalCode(err) != "internal" {
			t.Fatal("unknown refusal escaped closed categories")
		}
		if err != nil && strings.Contains(RefusalCode(err), "private") {
			t.Fatal("private cause exposed")
		}
	}
}
