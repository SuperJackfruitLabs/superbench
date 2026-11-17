package storageproof

import (
	"strings"
	"testing"
)

const testUUID = "7eb11321-0f04-4bec-91bb-3dff4119768c"

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
	digest, err := AuthorityDigest(map[string]string{"Root": "/srv/superbench-data"}, Root{Device: device, Inode: 128, Mode: 0x800001ed}, Root{Device: 0x801, Inode: 4242, Mode: 0x800001c0}, mount, uuid, "header", "anchor")
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
	data, ledger := Root{Device: 0x701, Inode: 128, Mode: 0x800001ed}, Root{Device: 0x801, Inode: 4242, Mode: 0x800001c0}
	policy := map[string]string{"Root": "/srv/superbench-data"}
	for name, change := range map[string]func(*Root, *Root, *string){
		"data root inode":     func(d, _ *Root, _ *string) { d.Inode++ },
		"data root owner":     func(d, _ *Root, _ *string) { d.UID = 1 },
		"data root mode":      func(d, _ *Root, _ *string) { d.Mode = 0x800001ff },
		"ledger device":       func(_, l *Root, _ *string) { l.Device++ },
		"ledger inode":        func(_, l *Root, _ *string) { l.Inode++ },
		"ledger header":       func(_, _ *Root, h *string) { *h = "other" },
		"same root, no field": func(*Root, *Root, *string) {},
	} {
		d, l, header := data, ledger, "header"
		change(&d, &l, &header)
		got, err := AuthorityDigest(policy, d, l, mount, uuid, header, "anchor")
		if err != nil {
			t.Fatal(err)
		}
		if (got == base) != (name == "same root, no field") {
			t.Errorf("%s: authority equality %v", name, got == base)
		}
	}
	if _, err := AuthorityDigest(policy, data, ledger, "99 "+testUUID+" / /srv/superbench-data 7:1", uuid, "header", "anchor"); err == nil {
		t.Error("an unparsed mount line was hashed")
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
