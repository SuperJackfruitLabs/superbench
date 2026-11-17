package main

import (
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestReclaimRunsOnlyUnderTheWorkspacesReclaimIdentity(t *testing.T) {
	s := payloadbootstrap.Spec{Key: "card", ID: strings.Repeat("c", 32)}
	h := strings.Repeat("a", 64)
	good := quotahelper.ReclaimIdentity(s.Key, s.ID, h, strings.Repeat("b", 64), strings.Repeat("d", 64))
	if err := checkReclaimIdentity(s, good); err != nil {
		t.Fatal(err)
	}
	create := good
	create.OperationID = strings.Repeat("e", 64)
	epoch := good
	epoch.Epoch = strings.Repeat("e", 32)
	other := s
	other.ID = strings.Repeat("f", 32)
	for name, c := range map[string]struct {
		s payloadbootstrap.Spec
		i quotaoperation.Identity
	}{"create-identity": {s, create}, "other-epoch": {s, epoch}, "other-workspace": {other, good}} {
		if err := checkReclaimIdentity(c.s, c.i); !errors.Is(err, errReclaimIdentity) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}
