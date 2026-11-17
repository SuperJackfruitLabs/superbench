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
const AuthorityDomain = "superbench.quota-operation-authority.v2"

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

// Root is the stat identity of a root-owned storage directory.
type Root struct {
	Device, Inode  uint64
	Mode, UID, GID uint32
}

// AuthorityDigest hashes the immutable helper authority over a managed volume.
// Every input is the same in every mount namespace and after a reboot that
// mounts the same filesystem. The data root's device number is left out because
// a loop or block device can be renumbered at boot; the filesystem is bound by
// its UUID in mount, in the ledger header and by the anchor on the volume. The
// ledger lives on the host's state filesystem, whose device the journal's ready
// record pins as well.
func AuthorityDigest(policy any, data, ledger Root, mount, uuid, header, anchor string) (string, error) {
	if !strings.HasPrefix(mount, "xfs uuid="+uuid+" ") || !volumePattern.MatchString(uuid) {
		return "", errors.New("authority requires a managed mount identity")
	}
	type stableRoot struct {
		Inode          uint64
		Mode, UID, GID uint32
	}
	raw, err := json.Marshal(struct {
		Domain                      string
		Policy                      any
		Data                        stableRoot
		Ledger                      Root
		Mount, UUID, Header, Anchor string
	}{AuthorityDomain, policy, stableRoot{data.Inode, data.Mode, data.UID, data.GID}, ledger, mount, uuid, header, anchor})
	if err != nil {
		return "", err
	}
	return Digest(raw), nil
}
