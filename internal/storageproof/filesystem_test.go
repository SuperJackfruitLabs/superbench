package storageproof

import (
	"errors"
	"testing"
)

const stateUUID = "0d4f6c2e-5b8a-4c1e-9a7d-2f3b4c5d6e7f"

func stateIdentity(t *testing.T, line string, device uint64, fsid [2]int32) string {
	t.Helper()
	identity, err := ParseStateFilesystem([]byte(line+"\n"), device, fsid)
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return identity
}

// findmnt --raw --target --output StateFilesystemColumns for one state
// filesystem, before and after reboots that renumber its device.
func TestStateFilesystemSurvivesRenumberedDevices(t *testing.T) {
	for name, boots := range map[string][]struct {
		line   string
		device uint64
		fsid   [2]int32
	}{
		"btrfs subvolume": {{"btrfs 0:31 " + stateUUID, 31, [2]int32{0x1234, -7}}, {"btrfs 0:44 " + stateUUID, 44, [2]int32{0x1234, -7}}},
		"ext4 on LVM":     {{"ext4 253:1 " + stateUUID, 253<<8 | 1, [2]int32{1, 2}}, {"ext4 253:3 " + stateUUID, 253<<8 | 3, [2]int32{1, 2}}},
		// XFS derives f_fsid from the device, so it must not be part of the identity.
		"xfs on a loop device": {{"xfs 7:0 " + stateUUID, 0x700, [2]int32{0x700, 0}}, {"xfs 7:4 " + stateUUID, 0x704, [2]int32{0x704, 0}}},
	} {
		if stateIdentity(t, boots[0].line, boots[0].device, boots[0].fsid) != stateIdentity(t, boots[1].line, boots[1].device, boots[1].fsid) {
			t.Errorf("%s: a renumbered device changed the identity", name)
		}
	}
}

func TestStateFilesystemDiffersForAnotherVolume(t *testing.T) {
	base := stateIdentity(t, "btrfs 0:31 "+stateUUID, 31, [2]int32{1, 2})
	for name, other := range map[string]string{
		"other UUID":      stateIdentity(t, "btrfs 0:31 11111111-1111-1111-1111-111111111111", 31, [2]int32{1, 2}),
		"other subvolume": stateIdentity(t, "btrfs 0:31 "+stateUUID, 31, [2]int32{1, 3}),
		"other type":      stateIdentity(t, "ext4 0:31 "+stateUUID, 31, [2]int32{1, 2}),
	} {
		if other == base {
			t.Errorf("%s kept the identity", name)
		}
	}
}

func TestStateFilesystemRefusesWhatABootCannotFindAgain(t *testing.T) {
	// unstable: refused as a filesystem that has no reboot-stable identity, the
	// install-time message; the others are refused as an unidentified mount.
	for name, c := range map[string]struct {
		line     string
		unstable bool
	}{
		"tmpfs":                 {"tmpfs 8:1", true},
		"overlay":               {"overlay 8:1", true},
		"no UUID":               {"ext4 8:1", true},
		"short UUID":            {"ext4 8:1 ABCD-1234", true},
		"vfat":                  {"vfat 8:1 " + stateUUID, true},
		"zfs":                   {"zfs 8:1 12345678901234567", true},
		"nested subvolume":      {"btrfs 0:30 " + stateUUID, false},
		"stacked over the path": {"xfs 8:2 " + stateUUID, false},
		"two mounts":            {"ext4 8:1 " + stateUUID + "\next4 8:1 " + stateUUID, false},
		"no output":             {"", false},
		"extra column":          {"ext4 8:1 " + stateUUID + " x", false},
		"malformed device":      {"ext4 8 " + stateUUID, false},
	} {
		_, err := ParseStateFilesystem([]byte(c.line+"\n"), 0x801, [2]int32{})
		if err == nil {
			t.Errorf("%s accepted", name)
		} else if errors.Is(err, ErrNoStableFilesystem) != c.unstable {
			t.Errorf("%s: unexpected refusal %v", name, err)
		}
	}
}
