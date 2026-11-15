package hostidentity

import (
	"strings"
	"testing"
)

func TestLinuxIdentityRetainsManagedExecutionDomain(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	got, err := linuxIdentity([]byte(id + "\n"))
	if err != nil || got != hash("superbench-host:", id) {
		t.Fatal("Linux handoff identity differs from existing execution identity", err)
	}
	for _, bad := range []string{"", strings.Repeat("0", 32), strings.ToUpper(id), id + "\n" + id, strings.Repeat(" ", 129) + id, "host-label"} {
		if _, err := linuxIdentity([]byte(bad)); err == nil {
			t.Fatal("accepted invalid machine identity")
		}
	}
}

func TestDarwinIdentityRejectsMissingAmbiguousAndUninitializedObservations(t *testing.T) {
	const id = "12345678-1234-ABCD-9876-0123456789AB"
	line := `    "IOPlatformUUID" = "` + id + `"` + "\n"
	got, err := darwinIdentity([]byte("+-o IOPlatformExpertDevice\n{\n" + line + "}\n"))
	if err != nil || got != hash("superbench-host-darwin:", strings.ToLower(id)) {
		t.Fatal("valid platform observation refused", err)
	}
	lower, err := darwinIdentity([]byte(strings.ReplaceAll(line, id, strings.ToLower(id))))
	if err != nil || lower != got {
		t.Fatal("platform UUID case changed the host identity")
	}
	for _, bad := range []string{"", line + line, strings.Repeat("x", 64<<10) + line,
		`"IOPlatformUUID" = "00000000-0000-0000-0000-000000000000"`,
		`"IOPlatformUUID" = "FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF"`,
		`"IOPlatformUUID" = "hostname"`, `"IOPlatformUUID" = "` + id + `" trailing`,
		`"Other" = "` + id + `"`} {
		if _, err := darwinIdentity([]byte(bad)); err == nil {
			t.Fatal("accepted invalid platform observation")
		}
	}
}
