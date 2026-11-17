//go:build linux && freshstoragefixture

package fixture

import (
	"strconv"
	"strings"
	"testing"
)

func TestLowerCredentialInventoryRejectsEveryIdentitySlot(t *testing.T) {
	good := "Name:	fixture-雪\nUid:	0 0 0 0\nGid:	0 0 0 0\nGroups:	\n"
	if err := lowerCredentialInventory([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for id := 61001; id <= 61004; id++ {
		for _, key := range []string{"Uid", "Gid"} {
			for slot := 0; slot < 4; slot++ {
				values := []string{"0", "0", "0", "0"}
				values[slot] = strconv.Itoa(id)
				raw := strings.Replace(good, key+":\t0 0 0 0", key+":\t"+strings.Join(values, " "), 1)
				if err := lowerCredentialInventory([]byte(raw)); err == nil || err.Error() != "fixture identity already used by process" {
					t.Fatalf("%s slot %d accepted: %v", key, slot, err)
				}
			}
		}
		raw := strings.Replace(good, "Groups:\t", "Groups:\t0 "+strconv.Itoa(id), 1)
		if err := lowerCredentialInventory([]byte(raw)); err == nil || err.Error() != "fixture identity already used by process" {
			t.Fatalf("supplementary group accepted: %v", err)
		}
	}
	for _, raw := range []string{"Uid:\t0 0 0 0\n", strings.Replace(good, "Uid:\t0 0 0 0", "Uid:\t0 0 0", 1), good + "Groups:\t0\n", strings.Replace(good, "Gid:\t0 0 0 0", "Gid:\t0 nope 0 0", 1)} {
		if err := lowerCredentialInventory([]byte(raw)); err == nil {
			t.Fatal("malformed inventory accepted")
		}
	}
}
