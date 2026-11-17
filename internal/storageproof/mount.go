package storageproof

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ManagedMountColumns is the findmnt --raw --output list ParseManagedMount reads.
const ManagedMountColumns = "ID,UUID,FSTYPE,FSROOT,TARGET,MAJ:MIN,FS-OPTIONS"

// AuthorityDomain names the helper authority digest. Version 1 hashed the mount
// ID and the data volume's device number, so a ready record written in one mount
// namespace (the installer's) never verified in another (the service unit's).
// Version 2 still hashed the ledger directory's device number, which btrfs and
// LVM can change at boot.
const AuthorityDomain = "superbench.quota-operation-authority.v3"

// DeviceNumber formats st_dev as the kernel's major:minor.
func DeviceNumber(device uint64) string {
	return fmt.Sprintf("%d:%d", (device>>8)&0xfff|(device>>32)&0xfffff000, device&0xff|(device>>12)&0xffffff00)
}

// ParseManagedMount checks findmnt's view of the dedicated data volume and
// returns the mount identity that storage proofs and the helper authority pin,
// and the filesystem UUID.
//
// The identity holds only what every mount namespace sees for the same mount
// and what survives a reboot that mounts the same filesystem again: the UUID,
// the type, the filesystem root, the target and enforced project quotas. The
// mount ID, the device number, per-mount (VFS) options and propagation peer
// groups are checked or ignored but never returned: a systemd unit's private
// namespace gives the same mount another ID and peer group, and a loop or block
// device number can change at boot. The exact root and target refuse a bind of
// a subdirectory or an ancestor, one line refuses a stacked mount, and nouuid is
// refused because only without it does the kernel keep a second XFS with the
// same UUID from being mounted while this one is.
func ParseManagedMount(raw []byte, root string, device uint64) (string, string, error) {
	f := strings.Fields(string(raw))
	if len(f) != 7 || !volumePattern.MatchString(f[1]) || f[2] != "xfs" || f[3] != "/" || f[4] != root || f[5] != DeviceNumber(device) {
		return "", "", errors.New("expected exact dedicated filesystem mount identity")
	}
	if id, err := strconv.ParseUint(f[0], 10, 64); err != nil || id == 0 {
		return "", "", errors.New("invalid mount ID")
	}
	options := map[string]bool{}
	for _, option := range strings.Split(f[6], ",") {
		options[option] = true
	}
	if !options["prjquota"] && !options["pquota"] {
		return "", "", errors.New("data volume is not mounted with enforced project quotas (prjquota)")
	}
	if options["nouuid"] {
		return "", "", errors.New("data volume is mounted with nouuid, so its UUID does not identify it")
	}
	uuid := strings.ToLower(f[1])
	return "xfs uuid=" + uuid + " fsroot=/ target=" + root + " prjquota", uuid, nil
}

// ParseUnmanagedMount finds the /proc/self/mountinfo line of the mount holding
// root and returns its namespace-independent part: device, filesystem root,
// mountpoint, type, source and superblock options. The mount and parent IDs,
// the per-mount options and the optional fields (peer groups) differ between
// mount namespaces and are left out.
func ParseUnmanagedMount(raw []byte, root string, device uint64) (string, error) {
	best, depth := "", -1
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 0 || len(fields) != separator+4 || fields[2] != DeviceNumber(device) {
			continue
		}
		mountpoint := decode.Replace(fields[4])
		if root != mountpoint && !strings.HasPrefix(root, strings.TrimSuffix(mountpoint, "/")+"/") {
			continue
		}
		if len(mountpoint) > depth {
			best, depth = strings.Join(append([]string{fields[2], fields[3], fields[4]}, fields[separator+1:]...), " "), len(mountpoint)
		}
	}
	if best == "" {
		return "", errors.New("actual storage mount could not be identified")
	}
	return "linux:" + best, nil
}

// Root is the stat identity of a root-owned storage directory.
type Root struct {
	Device, Inode  uint64
	Mode, UID, GID uint32
}

// AuthorityDigest hashes the immutable helper authority over a managed volume.
// Every input is the same in every mount namespace and after a reboot that
// mounts the same filesystems. No device number is hashed, because a loop, LVM
// or block device and a btrfs subvolume can be renumbered at boot: the data
// volume is bound by its UUID in mount, in the ledger header and by the anchor on
// the volume, and the ledger directory by ledgerFilesystem, the
// ParseStateFilesystem identity of the state filesystem that holds it.
func AuthorityDigest(policy any, data, ledger Root, ledgerFilesystem, mount, uuid, header, anchor string) (string, error) {
	if !strings.HasPrefix(mount, "xfs uuid="+uuid+" ") || !volumePattern.MatchString(uuid) {
		return "", errors.New("authority requires a managed mount identity")
	}
	if !strings.HasPrefix(ledgerFilesystem, "state ") {
		return "", errors.New("authority requires a stable state filesystem identity")
	}
	type stableRoot struct {
		Inode          uint64
		Mode, UID, GID uint32
	}
	raw, err := json.Marshal(struct {
		Domain                      string
		Policy                      any
		Data, Ledger                stableRoot
		LedgerFilesystem            string
		Mount, UUID, Header, Anchor string
	}{AuthorityDomain, policy, stableRoot{data.Inode, data.Mode, data.UID, data.GID}, stableRoot{ledger.Inode, ledger.Mode, ledger.UID, ledger.GID}, ledgerFilesystem, mount, uuid, header, anchor})
	if err != nil {
		return "", err
	}
	return Digest(raw), nil
}
