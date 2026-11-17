package storageproof

import (
	"errors"
	"fmt"
	"strings"
)

// StateFilesystemColumns is the findmnt --raw --target output list that
// ParseStateFilesystem reads. UUID is last because findmnt prints an empty last
// column as nothing at all.
const StateFilesystemColumns = "FSTYPE,MAJ:MIN,UUID"

// ErrNoStableFilesystem is a state directory on a filesystem that SuperBench
// cannot recognise again after a reboot. Initialization refuses it, so a host is
// never left with a ledger that the next boot would refuse.
var ErrNoStableFilesystem = errors.New("the SuperBench state directory must be on an ext4, XFS or btrfs filesystem with a UUID, because its device number can change at boot and only the UUID identifies it again; move /var/lib/superbench-quota to such a filesystem and initialize again")

// ParseStateFilesystem returns a reboot-stable identity for the filesystem that
// holds a state directory, from findmnt's view of its containing mount.
//
// The device number (st_dev) is never part of it: btrfs gives each subvolume an
// anonymous 0:NN number that can change at boot, and LVM minors follow activation
// order. The device is still compared, within this boot, with the mount findmnt
// names, so a nested btrfs subvolume or another mount stacked over the path is
// refused rather than identified as its parent. The identity holds the type and
// the superblock UUID; on btrfs, where one UUID covers every subvolume, it also
// holds statfs f_fsid, which btrfs derives from the UUID and the subvolume.
// Neither the target nor the filesystem root is included: a systemd unit's
// ReadWritePaths bind gives the same mount another target and root.
func ParseStateFilesystem(raw []byte, device uint64, fsid [2]int32) (string, error) {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	f := strings.Fields(lines[0])
	if len(lines) != 1 || len(f) < 2 || len(f) > 3 || f[1] != DeviceNumber(device) {
		return "", errors.New("state directory mount could not be identified")
	}
	switch f[0] {
	case "ext2", "ext3", "ext4", "xfs", "btrfs":
	default:
		return "", fmt.Errorf("%w (found %s)", ErrNoStableFilesystem, f[0])
	}
	if len(f) != 3 || !volumePattern.MatchString(f[2]) {
		return "", fmt.Errorf("%w (found %s without a UUID)", ErrNoStableFilesystem, f[0])
	}
	identity := "state " + f[0] + " uuid=" + strings.ToLower(f[2])
	if f[0] == "btrfs" {
		identity += fmt.Sprintf(" fsid=%08x%08x", uint32(fsid[0]), uint32(fsid[1]))
	}
	return identity, nil
}
