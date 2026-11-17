package storageproof

import (
	"strings"
	"testing"
)

const testUUID = "7eb11321-0f04-4bec-91bb-3dff4119768c"
const testStateFS = "state ext4 uuid=0d4f6c2e-5b8a-4c1e-9a7d-2f3b4c5d6e7f"

// findmnt --raw --output ManagedMountColumns for one data volume, as the host
// namespace, a systemd unit's private namespace and the next boot report it.
var sameMount = []struct {
	name, line string
	device     uint64
}{
	{"installer host namespace", "99 " + testUUID + " xfs / /srv/superbench-data 7:1 rw,inode64,logbufs=8,logbsize=32k,prjquota", 0x701},
	{"service private namespace", "1293 " + testUUID + " xfs / /srv/superbench-data 7:1 rw,inode64,logbufs=8,logbsize=32k,prjquota", 0x701},
	{"after reboot on another loop device", "57 " + strings.ToUpper(testUUID) + " xfs / /srv/superbench-data 7:2 rw,attr2,inode64,logbufs=8,logbsize=32k,prjquota,usrquota", 0x702},
	{"block device", "31 " + testUUID + " xfs / /srv/superbench-data 259:3 rw,relatime,prjquota", 259<<8 | 3},
}

func managedDigest(t *testing.T, line string, device uint64) string {
	t.Helper()
	mount, uuid, err := ParseManagedMount([]byte(line+"\n"), "/srv/superbench-data", device)
	if err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	digest, err := AuthorityDigest(map[string]string{"Root": "/srv/superbench-data"}, Root{Device: device, Inode: 128, Mode: 0x800001ed}, Root{Device: 0x801, Inode: 4242, Mode: 0x800001c0}, testStateFS, mount, uuid, "header", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestAuthorityIsTheSameInEveryNamespaceAndAfterReboot(t *testing.T) {
	want := managedDigest(t, sameMount[0].line, sameMount[0].device)
	for _, m := range sameMount[1:] {
		if got := managedDigest(t, m.line, m.device); got != want {
			t.Errorf("%s: the same mounted filesystem changed the authority", m.name)
		}
	}
}

func TestManagedMountRefusesOtherMounts(t *testing.T) {
	for name, line := range map[string]string{
		"ancestor mount":         "99 " + testUUID + " xfs / / 7:1 rw,prjquota",
		"bind of a subdirectory": "99 " + testUUID + " xfs /benches /srv/superbench-data 7:1 rw,prjquota",
		"other device than stat": "99 " + testUUID + " xfs / /srv/superbench-data 7:2 rw,prjquota",
		"zero mount ID":          "0 " + testUUID + " xfs / /srv/superbench-data 7:1 rw,prjquota",
		"not XFS":                "99 " + testUUID + " ext4 / /srv/superbench-data 7:1 rw,prjquota",
		"no project quota":       "99 " + testUUID + " xfs / /srv/superbench-data 7:1 rw,inode64,usrquota",
		"quota not enforced":     "99 " + testUUID + " xfs / /srv/superbench-data 7:1 rw,inode64,pqnoenforce",
		"nouuid":                 "99 " + testUUID + " xfs / /srv/superbench-data 7:1 rw,nouuid,prjquota",
		"no UUID":                "99 xfs / /srv/superbench-data 7:1 rw,prjquota",
		"stacked mounts":         sameMount[0].line + "\n" + sameMount[1].line,
		"no options":             "99 " + testUUID + " xfs / /srv/superbench-data 7:1",
	} {
		if _, _, err := ParseManagedMount([]byte(line+"\n"), "/srv/superbench-data", 0x701); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAuthorityDiffersForAnotherVolumeOrRoot(t *testing.T) {
	base := managedDigest(t, sameMount[0].line, 0x701)
	if managedDigest(t, strings.Replace(sameMount[0].line, testUUID, "11111111-1111-1111-1111-111111111111", 1), 0x701) == base {
		t.Error("a volume with another UUID kept the authority")
	}
	mount, uuid, _ := ParseManagedMount([]byte(sameMount[0].line), "/srv/superbench-data", 0x701)
	type inputs struct {
		policy                          map[string]string
		data, ledger                    Root
		ledgerFS, mount, header, anchor string
	}
	for name, change := range map[string]func(*inputs){
		"policy":                   func(in *inputs) { in.policy = map[string]string{"Root": "/srv/other"} },
		"data root inode":          func(in *inputs) { in.data.Inode++ },
		"data root owner":          func(in *inputs) { in.data.UID = 1 },
		"data root group":          func(in *inputs) { in.data.GID = 1 },
		"data root mode":           func(in *inputs) { in.data.Mode = 0x800001ff },
		"ledger inode":             func(in *inputs) { in.ledger.Inode++ },
		"ledger owner":             func(in *inputs) { in.ledger.UID = 1 },
		"ledger group":             func(in *inputs) { in.ledger.GID = 1 },
		"ledger mode":              func(in *inputs) { in.ledger.Mode = 0x800001ff },
		"ledger on another volume": func(in *inputs) { in.ledgerFS = "state ext4 uuid=11111111-1111-1111-1111-111111111111" },
		"ledger header":            func(in *inputs) { in.header = "other" },
		"anchor":                   func(in *inputs) { in.anchor = "other" },
		// A reboot renumbers both devices; the authority must not change.
		"same root, renumbered devices": func(in *inputs) { in.data.Device, in.ledger.Device = 0x702, 0x2a },
		"same root, no field":           func(*inputs) {},
	} {
		in := inputs{map[string]string{"Root": "/srv/superbench-data"}, Root{Device: 0x701, Inode: 128, Mode: 0x800001ed}, Root{Device: 0x801, Inode: 4242, Mode: 0x800001c0}, testStateFS, mount, "header", "anchor"}
		change(&in)
		got, err := AuthorityDigest(in.policy, in.data, in.ledger, in.ledgerFS, in.mount, uuid, in.header, in.anchor)
		if err != nil {
			t.Fatal(err)
		}
		if (got == base) != strings.HasPrefix(name, "same root") {
			t.Errorf("%s: authority equality %v", name, got == base)
		}
	}
	data, ledger := Root{Device: 0x701, Inode: 128, Mode: 0x800001ed}, Root{Device: 0x801, Inode: 4242, Mode: 0x800001c0}
	if _, err := AuthorityDigest(nil, data, ledger, testStateFS, "99 "+testUUID+" / /srv/superbench-data 7:1", uuid, "header", "anchor"); err == nil {
		t.Error("an unparsed mount line was hashed")
	}
	if _, err := AuthorityDigest(nil, data, ledger, "8:1", mount, uuid, "header", "anchor"); err == nil {
		t.Error("a ledger without a stable filesystem identity was hashed")
	}
}

// /proc/self/mountinfo for one unmanaged data directory in the host namespace
// and in a private namespace: other mount and parent IDs, peer groups and
// per-mount options.
func TestUnmanagedMountIgnoresNamespaceLocalFields(t *testing.T) {
	host := "1 0 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw\n99 1 8:1 /srv/data /srv/data rw,relatime shared:22 - ext4 /dev/sda1 rw\n"
	private := "1301 1290 8:1 / / ro,relatime master:1 - ext4 /dev/sda1 rw\n1302 1301 8:1 /srv/data /srv/data rw,nosuid,nodev - ext4 /dev/sda1 rw\n"
	a, err := ParseUnmanagedMount([]byte(host), "/srv/data/bench", 0x801)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseUnmanagedMount([]byte(private), "/srv/data/bench", 0x801)
	if err != nil || a != b {
		t.Fatalf("namespace changed the unmanaged mount: %q %q %v", a, b, err)
	}
	for name, table := range map[string]string{
		"other filesystem root": strings.Replace(host, "/srv/data /srv/data", "/other /srv/data", 1),
		"other source":          strings.Replace(host, "shared:22 - ext4 /dev/sda1", "shared:22 - ext4 /dev/sdb1", 1),
		"superblock read-only":  strings.Replace(host, "shared:22 - ext4 /dev/sda1 rw", "shared:22 - ext4 /dev/sda1 ro", 1),
	} {
		if c, err := ParseUnmanagedMount([]byte(table), "/srv/data/bench", 0x801); err != nil || c == a {
			t.Errorf("%s kept the unmanaged mount: %v", name, err)
		}
	}
	if _, err := ParseUnmanagedMount([]byte(host), "/srv/data/bench", 0x802); err == nil {
		t.Error("a mount of another device was used")
	}
	// Only three fields may follow the separator: type, source, superblock options.
	for _, malformed := range []string{"99 1 8:1 /srv/data /srv/data rw shared:22 - ext4 /dev/sda1 rw extra\n", "99 1 8:1 /srv/data /srv/data rw shared:22 - ext4 /dev/sda1\n"} {
		if c, err := ParseUnmanagedMount([]byte(malformed), "/srv/data/bench", 0x801); err == nil {
			t.Errorf("a malformed mountinfo line was used: %q", c)
		}
	}
}

func TestVerifyQuotaRequiresOneExactRow(t *testing.T) {
	for _, bad := range []string{"", "#10000 0 0 42", "#10000 0 0 8\n#10000 0 0 8"} {
		if VerifyQuota([]byte(bad), 10000, 8) == nil {
			t.Fatal("unverified kernel cap accepted", bad)
		}
	}
	if VerifyQuota([]byte("#10000 0 0 8"), 10000, 8) != nil {
		t.Fatal("exact kernel cap refused")
	}
}
