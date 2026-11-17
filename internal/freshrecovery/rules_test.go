package freshrecovery

import (
	"errors"
	"path"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

const (
	daemonUID = 61001
	daemonGID = 61003
	device    = 8
	assigned  = 10004
)

var daemon = owners{daemonUID, daemonGID}

func dirNode(inode uint64, uid, gid uint32) Node {
	return Node{Device: device, Inode: inode, Mode: modeDir | 0o700, UID: uid, GID: gid}
}

func testSpec(members ...string) payloadbootstrap.Spec {
	s := payloadbootstrap.Spec{Version: payloadbootstrap.Version, Key: "card", ID: strings.Repeat("a", 32), CheckpointRepository: members[0]}
	for _, m := range members {
		s.Members = append(s.Members, payloadbootstrap.Member{Name: m, Repository: "https://example.invalid/" + m + ".git", Branch: "task/" + m, Source: strings.Repeat("b", 40), ObjectFormat: "sha1"})
	}
	return s
}

// fixedEntries builds the first n fixed directories as Begin leaves them after a
// crash before the last chown: daemon-owned except the last, still root-owned.
func fixedEntries(fixed []string, n int) []Entry {
	var entries []Entry
	for k := range n {
		uid, gid := uint32(daemonUID), uint32(daemonGID)
		if k == n-1 {
			uid, gid = 0, 0
		}
		entries = append(entries, Entry{fixed[k], dirNode(uint64(100+k), uid, gid)})
	}
	return entries
}

func TestLeafPhaseOwnerAndModeRules(t *testing.T) {
	fixed := payloadbootstrap.FixedDirectories(testSpec("api", "web"))
	recorded := [2]uint64{device, 50}
	root := dirNode(50, 0, 0)
	daemonLeaf := dirNode(50, daemonUID, daemonGID)
	type c struct {
		phase    string
		recorded [2]uint64
		leaf     Leaf
	}
	for name, v := range map[string]c{
		"reserved project zero":     {"reserved", [2]uint64{}, Leaf{Node: dirNode(77, 0, 0)}},
		"reserved assigned project": {"reserved", [2]uint64{}, Leaf{Node: dirNode(77, 0, 0), ProjectID: assigned}},
		"allocating (a)":            {"allocating", recorded, Leaf{Node: root, ProjectID: assigned}},
		"allocating (b)":            {"allocating", recorded, Leaf{Node: root, ProjectID: assigned, Fixed: fixedEntries(fixed, 4)}},
		"allocating (b) all fixed":  {"allocating", recorded, Leaf{Node: root, ProjectID: assigned, Fixed: fixedEntries(fixed, len(fixed))}},
		"allocating (c) empty":      {"allocating", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
		"begun empty":               {"begun", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
		"staging empty":             {"staging", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
		"materialized empty":        {"materialized_unfenced", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
		"staged empty":              {"staged", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
	} {
		if err := leafAllowed(v.phase, v.recorded, assigned, daemon, device, fixed, v.leaf); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	mode := func(n Node, m uint32) Node { n.Mode = m; return n }
	moved := func(n Node) Node { n.Inode++; return n }
	foreignDevice := func(n Node) Node { n.Device++; return n }
	for name, v := range map[string]c{
		"reserved foreign project":    {"reserved", [2]uint64{}, Leaf{Node: dirNode(77, 0, 0), ProjectID: assigned + 1}},
		"reserved daemon owned":       {"reserved", [2]uint64{}, Leaf{Node: dirNode(77, daemonUID, daemonGID)}},
		"reserved root group daemon":  {"reserved", [2]uint64{}, Leaf{Node: dirNode(77, 0, daemonGID)}},
		"reserved with recorded root": {"reserved", recorded, Leaf{Node: root}},
		"reserved with fixed names":   {"reserved", [2]uint64{}, Leaf{Node: dirNode(77, 0, 0), Fixed: fixedEntries(fixed, 1)}},
		"reserved mode 0755":          {"reserved", [2]uint64{}, Leaf{Node: mode(dirNode(77, 0, 0), modeDir|0o755)}},
		"reserved setgid":             {"reserved", [2]uint64{}, Leaf{Node: mode(dirNode(77, 0, 0), modeDir|0o2700)}},
		"reserved not a directory":    {"reserved", [2]uint64{}, Leaf{Node: mode(dirNode(77, 0, 0), 0o100700)}},
		"reserved other device":       {"reserved", [2]uint64{}, Leaf{Node: foreignDevice(dirNode(77, 0, 0))}},
		"allocating moved leaf":       {"allocating", recorded, Leaf{Node: moved(root), ProjectID: assigned}},
		"allocating project zero":     {"allocating", recorded, Leaf{Node: root}},
		"allocating no recorded root": {"allocating", [2]uint64{}, Leaf{Node: root, ProjectID: assigned}},
		"allocating foreign owner":    {"allocating", recorded, Leaf{Node: dirNode(50, 1234, 1234), ProjectID: assigned}},
		"allocating (c) with fixed":   {"allocating", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned, Fixed: fixedEntries(fixed, 1)}},
		"begun root owned":            {"begun", recorded, Leaf{Node: root, ProjectID: assigned}},
		"begun with fixed names":      {"begun", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned, Fixed: fixedEntries(fixed, 2)}},
		"staged moved leaf":           {"staged", recorded, Leaf{Node: moved(daemonLeaf), ProjectID: assigned}},
		"transferring":                {"transferring", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
		"completed":                   {"completed", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
		"unknown phase":               {"cleaning", recorded, Leaf{Node: daemonLeaf, ProjectID: assigned}},
	} {
		if err := leafAllowed(v.phase, v.recorded, assigned, daemon, device, fixed, v.leaf); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
	if err := leafAllowed("reserved", [2]uint64{}, assigned, owners{0, 0}, device, fixed, Leaf{Node: dirNode(77, 0, 0)}); !errors.Is(err, ErrRecovery) {
		t.Errorf("a root daemon identity admitted: %v", err)
	}
}

func TestFixedPrefixAdmitsOnlyBeginsOwnDirectories(t *testing.T) {
	fixed := payloadbootstrap.FixedDirectories(testSpec("api"))
	if err := fixedPrefix(fixed, nil, device, daemon); err != nil {
		t.Fatal("empty prefix refused", err)
	}
	allDaemon := fixedEntries(fixed, 3)
	allDaemon[2].Node.UID, allDaemon[2].Node.GID = daemonUID, daemonGID
	if err := fixedPrefix(fixed, allDaemon, device, daemon); err != nil {
		t.Fatal("daemon-owned prefix refused", err)
	}
	bad := map[string]func([]Entry) []Entry{
		"root before last": func(e []Entry) []Entry { e[0].Node.UID, e[0].Node.GID = 0, 0; return e },
		"foreign owner":    func(e []Entry) []Entry { e[1].Node.UID = 4242; return e },
		"gap":              func(e []Entry) []Entry { e[1].Name = fixed[2]; return e },
		"unknown name":     func(e []Entry) []Entry { e[2].Name = ".superbench-runtime/payload"; return e },
		"mode":             func(e []Entry) []Entry { e[1].Node.Mode = modeDir | 0o750; return e },
		"symlink":          func(e []Entry) []Entry { e[1].Node.Mode = 0o120777; return e },
		"device":           func(e []Entry) []Entry { e[1].Node.Device++; return e },
		"same inode twice": func(e []Entry) []Entry { e[1].Node.Inode = e[0].Node.Inode; return e },
		"zero inode":       func(e []Entry) []Entry { e[2].Node.Inode = 0; return e },
		"too many": func(e []Entry) []Entry {
			return fixedEntries(append(append([]string{}, fixed...), "extra"), len(fixed)+1)
		},
	}
	for name, mutate := range bad {
		if err := fixedPrefix(fixed, mutate(fixedEntries(fixed, 3)), device, daemon); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
}

func TestFixedDirectoriesCreateParentsFirstSoReverseRemovesChildrenFirst(t *testing.T) {
	fixed := payloadbootstrap.FixedDirectories(testSpec("api", "web"))
	want := []string{".superbench-git", ".superbench-runtime", ".superbench-runtime/.root", ".superbench-runtime/.root/home", ".superbench-runtime/.root/tmp",
		".superbench-runtime/api", ".superbench-runtime/api/home", ".superbench-runtime/api/tmp", ".superbench-runtime/web", ".superbench-runtime/web/home", ".superbench-runtime/web/tmp"}
	if !slices.Equal(fixed, want) {
		t.Fatalf("Begin's fixed directories changed: %q", fixed)
	}
	for k, name := range fixed {
		if parent := path.Dir(name); parent != "." && !slices.Contains(fixed[:k], parent) {
			t.Fatalf("%s precedes its parent %s", name, parent)
		}
	}
	got := children(fixed[:4])
	if len(got) != 5 || !slices.Equal(got["."], []string{".superbench-git", ".superbench-runtime"}) || !slices.Equal(got[".superbench-runtime"], []string{".root"}) ||
		!slices.Equal(got[".superbench-runtime/.root"], []string{"home"}) || len(got[".superbench-git"]) != 0 || len(got[".superbench-runtime/.root/home"]) != 0 {
		t.Fatalf("expected children differ: %q", got)
	}
	if got := children(nil); len(got) != 1 || len(got["."]) != 0 {
		t.Fatalf("an empty leaf must hold nothing: %q", got)
	}
}

func TestPrefixOfAcceptsOnlyARemovedSuffix(t *testing.T) {
	fixed := payloadbootstrap.FixedDirectories(testSpec("api"))
	planned := fixedEntries(fixed, 4)
	for n := range len(planned) + 1 {
		if !prefixOf(planned[:n], planned) {
			t.Fatalf("prefix %d refused", n)
		}
	}
	changed := slices.Clone(planned[:2])
	changed[1].Node.Inode++
	if prefixOf(changed, planned) || prefixOf(fixedEntries(fixed, 5), planned) || prefixOf(planned[1:3], planned) {
		t.Fatal("a changed, longer or shifted prefix admitted")
	}
}

func TestClassifyAcceptsNoRowOrTheExactOriginalCaps(t *testing.T) {
	for raw, want := range map[string]rows{
		"":                                  noRow,
		"\n\n":                              noRow,
		"#10004 0 0 4096 00 [--------]\n":   exactRow,
		"#10004          0          0 4096": exactRow,
	} {
		if got, err := classify([]byte(raw), assigned, 4096); err != nil || got != want {
			t.Errorf("%q: %v %v", raw, got, err)
		}
	}
	for name, raw := range map[string]string{
		"foreign cap":    "#10004 0 0 8192\n",
		"zero cap":       "#10004 0 0 0\n",
		"soft cap":       "#10004 0 1024 4096\n",
		"usage":          "#10004 1 0 4096\n",
		"duplicate":      "#10004 0 0 4096\n#10004 0 0 4096\n",
		"truncated":      "#10004 0 0\n",
		"truncated dup":  "#10004 0 0 4096\n#10004\n",
		"other project":  "#10005 0 0 4096\n",
		"header":         "Project quota on /srv/data\n#10004 0 0 4096\n",
		"invalid usage":  "#10004 x 0 4096\n",
		"invalid soft":   "#10004 0 - 4096\n",
		"invalid cap":    "#10004 0 0 4k\n",
		"negative usage": "#10004 -1 0 4096\n",
	} {
		if _, err := classify([]byte(raw), assigned, 4096); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s admitted: %q %v", name, raw, err)
		}
	}
}

func TestQuotaStateRequiresAccountingAndEnforcementOn(t *testing.T) {
	on := "Project quota state on /srv/data (/dev/vdb)\n  Accounting: ON\n  Enforcement: ON\n  Inode: #131 (1 blocks, 1 extents)\n"
	if err := quotaOn([]byte(on)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"accounting off":  strings.Replace(on, "Accounting: ON", "Accounting: OFF", 1),
		"enforcement off": strings.Replace(on, "Enforcement: ON", "Enforcement: OFF", 1),
		"missing":         "",
		"no enforcement":  "  Accounting: ON\n",
		"twice":           on + on,
		"mixed":           on + "  Enforcement: OFF\n",
	} {
		if err := quotaOn([]byte(raw)); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
}

func TestMountIdentityRequiresTheDedicatedRootAndLedgerUUID(t *testing.T) {
	uuid := "11111111-2222-3333-4444-555555555555"
	if err := mountIdentity([]byte("/srv/data / "+strings.ToUpper(uuid)+"\n"), "/srv/data", uuid); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"parent mount": "/srv / " + uuid,
		"bind subtree": "/srv/data /data " + uuid,
		"other uuid":   "/srv/data / 11111111-2222-3333-4444-555555555556",
		"no uuid":      "/srv/data /",
		"two mounts":   "/srv/data / " + uuid + "\n/srv/data / " + uuid,
	} {
		if err := mountIdentity([]byte(raw), "/srv/data", uuid); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s admitted: %v", name, err)
		}
	}
	if err := mountIdentity([]byte("/srv/data / "), "/srv/data", ""); !errors.Is(err, ErrRecovery) {
		t.Error("an empty ledger UUID admitted")
	}
}

func TestLimitCommandSetsOnlyTheOriginalHardCaps(t *testing.T) {
	if got := limitCommand(10004, 4<<20, 4096); got != "limit -p bhard=4096k ihard=4096 10004" {
		t.Fatalf("limit command changed: %q", got)
	}
}

func TestFsxattrMatchesTheKernelABI(t *testing.T) {
	if unsafe.Sizeof(fsxattr{}) != fsxattrSize || unsafe.Offsetof(fsxattr{}.Projid) != 12 || fsIocFsgetxattr != 0x801c581f {
		t.Fatalf("FS_IOC_FSGETXATTR ABI differs: size %d offset %d request %#x", unsafe.Sizeof(fsxattr{}), unsafe.Offsetof(fsxattr{}.Projid), fsIocFsgetxattr)
	}
}
