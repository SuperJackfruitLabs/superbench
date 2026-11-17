package payloadbootstrap

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestReclaimMovesACompletedIntentForwardOnly(t *testing.T) {
	for _, ok := range [][2]string{{"completed", "reclaiming"}, {"reclaiming", "reclaimed"}, {"reclaiming", "reclaiming"}, {"reclaimed", "reclaimed"}} {
		if !reclaimPhase(ok[0], ok[1]) {
			t.Errorf("%s -> %s refused", ok[0], ok[1])
		}
	}
	for _, bad := range [][2]string{{"completed", "reclaimed"}, {"completed", "completed"}, {"reclaimed", "reclaiming"}, {"reclaiming", "completed"}, {"staged", "reclaiming"}, {"transferring", "reclaiming"}, {"reserved", "reclaiming"}, {"completed", "staged"}} {
		if reclaimPhase(bad[0], bad[1]) {
			t.Errorf("%s -> %s accepted", bad[0], bad[1])
		}
	}
	id := func(c string) quotaoperation.Identity {
		h := strings.Repeat(c, 64)
		return quotaoperation.Identity{OperationID: h, Epoch: h[:32], StateIdentity: h, Authority: strings.Repeat("a", 64), BinarySHA256: h}
	}
	create, reclaim, foreign := id("1"), id("2"), id("3")
	foreign.Authority = strings.Repeat("f", 64)
	if !reclaimable(true, create, reclaim, "completed") || !reclaimable(true, create, reclaim, "reclaimed") {
		t.Fatal("a reclaim manager refused the completed create's intent")
	}
	if reclaimable(false, create, reclaim, "completed") || reclaimable(true, foreign, reclaim, "completed") || reclaimable(true, create, reclaim, "staged") || reclaimable(true, quotaoperation.Identity{}, reclaim, "completed") {
		t.Fatal("another operation's intent admitted outside a completed reclaim")
	}
}
