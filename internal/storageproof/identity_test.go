package storageproof

import (
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

func retainedIdentity(managed bool) Identity {
	s := Identity{Version: Version, Root: "/srv/superbench-data", BenchKey: "api", Device: 0x701, Inode: 128,
		Assignment: quotahelper.Assignment{Path: "/srv/superbench-data/benches/api"}}
	if !managed {
		s.Mount = "linux:7:1 / /srv/superbench-data xfs /dev/loop1 rw"
		return s
	}
	digest := Digest([]byte("x"))
	s.Mount, s.Managed, s.FilesystemUUID = "xfs uuid="+testUUID+" fsroot=/ target=/srv/superbench-data prjquota", true, testUUID
	s.PolicySHA256, s.LedgerSHA256, s.AnchorSHA256, s.ConfigSHA256 = digest, digest, digest, digest
	s.Assignment.ProjectID, s.Assignment.Bytes, s.Assignment.Inodes = 10000, 1<<20, 100
	return s
}

// A reboot that attaches the data image at another loop minor keeps a managed
// identity; a different volume or root never matches.
func TestManagedIdentitySurvivesARenumberedDevice(t *testing.T) {
	retained := retainedIdentity(true)
	if err := retained.Validate(); err != nil {
		t.Fatal(err)
	}
	rebooted := retained
	rebooted.Device = 0x704
	if err := retained.Matches(rebooted); err != nil {
		t.Fatalf("renumbered loop device refused: %v", err)
	}
	for name, change := range map[string]func(*Identity){
		"other volume UUID": func(s *Identity) {
			s.FilesystemUUID = "11111111-1111-1111-1111-111111111111"
			s.Mount = "xfs uuid=11111111-1111-1111-1111-111111111111 fsroot=/ target=/srv/superbench-data prjquota"
		},
		"other root inode": func(s *Identity) { s.Inode++ },
		"other anchor":     func(s *Identity) { s.AnchorSHA256 = Digest([]byte("y")) },
		"other assignment": func(s *Identity) { s.Assignment.ProjectID++ },
	} {
		current := rebooted
		change(&current)
		if retained.Matches(current) == nil {
			t.Errorf("%s matched", name)
		}
	}
}

func TestUnmanagedIdentityRefusesARenumberedDeviceWithGuidance(t *testing.T) {
	retained := retainedIdentity(false)
	if err := retained.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := retained.Matches(retained); err != nil {
		t.Fatal(err)
	}
	current := retained
	current.Device, current.Mount = 0x704, "linux:7:4 / /srv/superbench-data xfs /dev/loop4 rw"
	if err := retained.Matches(current); !errors.Is(err, ErrUnmanagedDevice) {
		t.Fatalf("renumbered unmanaged device: %v", err)
	}
}
