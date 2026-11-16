package serviceidentity

import (
	"errors"
	"os"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

func TestZeroHelperRegistryCannotSupplyPolicy(t *testing.T) {
	var h HelperRegistry
	if err := h.Recheck(); !errors.Is(err, ErrAuthority) {
		t.Fatalf("zero helper snapshot: %v", err)
	}
	if p, err := h.Policy(); p != (payloadpolicy.Policy{}) || !errors.Is(err, ErrAuthority) {
		t.Fatalf("zero helper policy: %#v %v", p, err)
	}
}

func TestOrdinaryIdentityCannotResolveRootHelperRegistry(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("helper caller refusal must run unprivileged")
	}
	h, err := ResolveHelperRegistry()
	if h != (HelperRegistry{}) || !errors.Is(err, ErrAuthority) {
		t.Fatalf("ordinary helper resolution: %#v %v", h, err)
	}
}
