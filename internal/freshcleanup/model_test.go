package freshcleanup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

// These are pure metadata fixtures, not observed root custody, actual payload
// credentials, controller quiescence or authority to delete any filesystem.
func fixture() (payloadpolicy.Policy, Binding, Observation) {
	p := payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/srv/data", StateRoot: "/var/lib/bench", CgroupRoot: "/sys/fs/cgroup/bench"}
	raw, _ := p.Bytes()
	b := Binding{Key: "card", ID: strings.Repeat("b", 32), PolicySHA256: digest(raw), SpecSHA256: strings.Repeat("c", 64), IntentSHA256: strings.Repeat("d", 64), Operation: quotaoperation.Identity{OperationID: strings.Repeat("1", 64), Epoch: strings.Repeat("2", 32), StateIdentity: strings.Repeat("3", 64), Authority: strings.Repeat("4", 64), BinarySHA256: strings.Repeat("5", 64)}, RootDevice: 8, RootInode: 10}
	entry := func(name string, ino uint64, mode os.FileMode) reap.Entry {
		e := reap.Entry{Path: name, Mode: uint32(mode), Device: 8, Inode: ino, SHA256: digest(nil)}
		if !mode.IsDir() {
			e.SHA256 = digest([]byte(name))
			e.GitSHA1 = strings.Repeat("a", 40)
			e.GitSHA256 = strings.Repeat("b", 64)
		}
		return e
	}
	tree := reap.Tree{Root: entry(".", 10, os.ModeDir|0700), Entries: []reap.Entry{entry("a", 11, os.ModeDir|0700), entry("a/file", 12, 0600), entry("a-z", 13, os.ModeSymlink|0777)}}
	o := Observation{Parent: Parent{Device: 8, Inode: 9, Mode: uint32(os.ModeDir | 0711)}, Tree: tree}
	for _, e := range append([]reap.Entry{tree.Root}, tree.Entries...) {
		n := uint64(1)
		if os.FileMode(e.Mode).IsDir() {
			n = 2
		}
		o.Owners = append(o.Owners, Ownership{Path: e.Path, Device: e.Device, Inode: e.Inode, Nlink: n, UID: 1001, GID: 1001})
	}
	return p, b, o
}

// The daemon owns the fixture tree; the assignment is a Fresh reservation.
var testOwner = Owner{UID: 1001, GID: 1001}
var testAssignment = Assignment{ProjectID: 10001, Bytes: 4 << 20, Inodes: 4096}

func requireRefusal(t *testing.T, p payloadpolicy.Policy, b Binding, o Observation) {
	t.Helper()
	a, err := New(p, b, testOwner, testAssignment, o)
	if !errors.Is(err, ErrApproval) || !reflect.DeepEqual(a, Approval{}) {
		t.Fatalf("invalid approval accepted: %v %#v", err, a)
	}
}

func TestApprovalCopiesAndRoundTripsCompleteOwnershipBoundTree(t *testing.T) {
	p, b, o := fixture()
	a, err := New(p, b, testOwner, testAssignment, o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.Bytes(p, b, testOwner, testAssignment)
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(pretty.Bytes(), p, b, testOwner, testAssignment)
	if err != nil || !reflect.DeepEqual(a, parsed) {
		t.Fatal("canonical round trip", err)
	}
	o.Tree.Entries[0].Path = "changed"
	o.Owners[0].UID = 0
	after, err := a.Bytes(p, b, testOwner, testAssignment)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("input aliases retained", err)
	}
	if a.RootPath != "/srv/data/benches/card" || len(a.Observation.Owners) != 4 {
		t.Fatal("incomplete root binding")
	}
}

func TestApprovalRefusesUnsafeOrIncompleteOwnershipAndTopology(t *testing.T) {
	for name, mutate := range map[string]func(*Observation){
		"root-uid":             func(o *Observation) { o.Owners[0].UID = 0 },
		"mixed-uid":            func(o *Observation) { o.Owners[2].UID = 1002 },
		"foreign-gid":          func(o *Observation) { o.Owners[2].GID = 1002 },
		"regular-hardlink":     func(o *Observation) { o.Owners[2].Nlink = 2 },
		"symlink-hardlink":     func(o *Observation) { o.Owners[3].Nlink = 2 },
		"zero-directory-links": func(o *Observation) { o.Owners[0].Nlink = 0 },
		"missing-owner":        func(o *Observation) { o.Owners = o.Owners[:3] },
		"extra-owner":          func(o *Observation) { o.Owners = append(o.Owners, o.Owners[0]) },
		"wrong-owner-node":     func(o *Observation) { o.Owners[1].Inode++ },
		"reordered-owners":     func(o *Observation) { o.Owners[1], o.Owners[2] = o.Owners[2], o.Owners[1] },
		"parent-uid":           func(o *Observation) { o.Parent.UID = 1002 },
		"parent-writable":      func(o *Observation) { o.Parent.Mode |= 0020 },
		"parent-alias":         func(o *Observation) { o.Parent.Inode = o.Tree.Root.Inode },
		"parent-device":        func(o *Observation) { o.Parent.Device++ },
		"changed-root":         func(o *Observation) { o.Tree.Root.Inode++; o.Owners[0].Inode++ },
		"root-permissions":     func(o *Observation) { o.Tree.Root.Mode |= 0044 },
		"foreign-device":       func(o *Observation) { o.Tree.Entries[1].Device++; o.Owners[2].Device++ },
		"duplicate-node":       func(o *Observation) { o.Tree.Entries[1].Inode = 11; o.Owners[2].Inode = 11 },
		"duplicate-path":       func(o *Observation) { o.Tree.Entries[1].Path = "a"; o.Owners[2].Path = "a" },
		"escape": func(o *Observation) {
			o.Tree.Entries[1].Path = "a/../../escape"
			o.Owners[2].Path = o.Tree.Entries[1].Path
		},
		"absolute":     func(o *Observation) { o.Tree.Entries[1].Path = "/escape"; o.Owners[2].Path = "/escape" },
		"invalid-utf8": func(o *Observation) { o.Tree.Entries[1].Path = "a/\xff"; o.Owners[2].Path = "a/\xff" },
		"orphan":       func(o *Observation) { o.Tree.Entries[1].Path = "missing/file"; o.Owners[2].Path = "missing/file" },
		"non-directory-parent": func(o *Observation) {
			o.Tree.Entries[0].Mode = 0600
			o.Tree.Entries[0].GitSHA1 = strings.Repeat("a", 40)
			o.Tree.Entries[0].GitSHA256 = strings.Repeat("b", 64)
			o.Owners[1].Nlink = 1
		},
		"special-fifo":      func(o *Observation) { o.Tree.Entries[1].Mode = uint32(os.ModeNamedPipe | 0600) },
		"setuid":            func(o *Observation) { o.Tree.Entries[1].Mode |= uint32(os.ModeSetuid) },
		"append-mode":       func(o *Observation) { o.Tree.Entries[1].Mode |= uint32(os.ModeAppend) },
		"recoverable-claim": func(o *Observation) { o.Tree.Entries[1].Recoverable = true },
		"missing-content":   func(o *Observation) { o.Tree.Entries[1].SHA256 = "" },
		"directory-content": func(o *Observation) { o.Tree.Entries[0].GitSHA1 = strings.Repeat("a", 40) },
		"global-sort-breaks-traversal": func(o *Observation) {
			o.Tree.Entries[1], o.Tree.Entries[2] = o.Tree.Entries[2], o.Tree.Entries[1]
			o.Owners[2], o.Owners[3] = o.Owners[3], o.Owners[2]
		},
	} {
		t.Run(name, func(t *testing.T) { p, b, o := fixture(); mutate(&o); requireRefusal(t, p, b, o) })
	}
}

func TestApprovalBindsOriginalEvidenceAndFixedPolicy(t *testing.T) {
	for name, change := range map[string]func(*Binding){
		"key": func(b *Binding) { b.Key = "other" }, "id": func(b *Binding) { b.ID = strings.Repeat("f", 32) },
		"spec": func(b *Binding) { b.SpecSHA256 = strings.Repeat("f", 64) }, "intent": func(b *Binding) { b.IntentSHA256 = strings.Repeat("f", 64) },
		"operation": func(b *Binding) { b.Operation.OperationID = strings.Repeat("f", 64) }, "epoch": func(b *Binding) { b.Operation.Epoch = strings.Repeat("f", 32) },
		"state": func(b *Binding) { b.Operation.StateIdentity = strings.Repeat("f", 64) }, "authority": func(b *Binding) { b.Operation.Authority = strings.Repeat("f", 64) }, "binary": func(b *Binding) { b.Operation.BinarySHA256 = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			p, b, o := fixture()
			other := b
			change(&other)
			a, err := New(p, other, testOwner, testAssignment, o)
			if err != nil {
				t.Fatal("valid alternate fixture", err)
			}
			if !errors.Is(a.Validate(p, b, testOwner, testAssignment), ErrApproval) {
				t.Fatal("substituted original evidence accepted")
			}
		})
	}
	p, b, o := fixture()
	a, err := New(p, b, testOwner, testAssignment, o)
	if err != nil {
		t.Fatal(err)
	}
	p.PayloadUID++
	if !errors.Is(a.Validate(p, b, testOwner, testAssignment), ErrApproval) {
		t.Fatal("changed policy accepted")
	}
	p, b, o = fixture()
	b.PolicySHA256 = strings.Repeat("f", 64)
	requireRefusal(t, p, b, o)
	p, b, o = fixture()
	b.Operation.Epoch = ""
	requireRefusal(t, p, b, o)
}

func TestDirectoryLinkCountsAreInitialObservationsNotOneLinkRules(t *testing.T) {
	p, b, o := fixture()
	a, err := New(p, b, testOwner, testAssignment, o)
	if err != nil {
		t.Fatal(err)
	}
	o.Owners[0].Nlink++
	o.Owners[1].Nlink++
	other, err := New(p, b, testOwner, testAssignment, o)
	if err != nil {
		t.Fatal("directory link count treated as regular-file link count", err)
	}
	if a.SHA256 == other.SHA256 {
		t.Fatal("initial directory observation not bound")
	}
	// Neither approval exposes a remaining-tree matcher or deletion method. A
	// future executor must account for directory link changes caused by deletion.
}

func TestApprovalCodecRefusesTamperingAndNonCanonicalDocuments(t *testing.T) {
	p, b, o := fixture()
	a, err := New(p, b, testOwner, testAssignment, o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.Bytes(p, b, testOwner, testAssignment)
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string][]byte{
		"duplicate":            append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"Version":"`+Version+`"}`)...),
		"unknown":              append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...),
		"tampered-owner":       bytes.Replace(raw, []byte(`"UID":1001`), []byte(`"UID":1002`), 1),
		"tampered-entry-owner": bytes.Replace(raw, []byte(`"Nlink":2,"UID":1001`), []byte(`"Nlink":2,"UID":1002`), 1),
		"tampered-project":     bytes.Replace(raw, []byte(`"ProjectID":10001`), []byte(`"ProjectID":10002`), 1),
		"tampered-digest":      bytes.Replace(raw, []byte(a.SHA256), []byte(strings.Repeat("0", 64)), 1),
		"trailing":             append(append([]byte{}, raw...), []byte(`{}`)...),
		"invalid-utf8":         append(raw, 0xff), "over-limit": bytes.Repeat([]byte(" "), MaxApproval+1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Parse(bad, p, b, testOwner, testAssignment)
			if !errors.Is(err, ErrApproval) || !reflect.DeepEqual(got, Approval{}) {
				t.Fatal("invalid document accepted", err)
			}
		})
	}
}

func TestApprovalRefusesEntryCountAndTotalEncodedBudget(t *testing.T) {
	p, b, o := fixture()
	o.Tree.Entries = make([]reap.Entry, MaxEntries+1)
	o.Owners = make([]Ownership, MaxEntries+2)
	requireRefusal(t, p, b, o)
	p, b, o = fixture()
	file, owner := o.Tree.Entries[1], o.Owners[2]
	o.Tree.Entries = nil
	o.Owners = o.Owners[:1]
	for i := 0; i < 4000; i++ {
		name := strings.Repeat("n", 2200) + fmt.Sprintf("%04d", i)
		e, v := file, owner
		e.Path, v.Path = name, name
		e.Inode, v.Inode = uint64(100+i), uint64(100+i)
		o.Tree.Entries = append(o.Tree.Entries, e)
		o.Owners = append(o.Owners, v)
	}
	requireRefusal(t, p, b, o)
}

func TestApprovalAcceptsOnlyTheDaemonOwnerAndTheExpectedAssignment(t *testing.T) {
	p, b, o := fixture()
	if _, err := New(p, b, testOwner, testAssignment, o); err != nil {
		t.Fatal("daemon-owned approval refused", err)
	}
	reown := func(o Observation, uid, gid uint32) Observation {
		o.Owners = append([]Ownership(nil), o.Owners...)
		for i := range o.Owners {
			o.Owners[i].UID, o.Owners[i].GID = uid, gid
		}
		return o
	}
	for name, owner := range map[string]Owner{
		"root":         {0, 0},
		"payload":      {p.PayloadUID, p.PayloadGID},
		"daemon-uid-0": {p.DaemonUID, 0},
		"other-gid":    {p.DaemonUID, p.PayloadGID},
	} {
		if a, err := New(p, b, owner, testAssignment, reown(o, owner.UID, owner.GID)); !errors.Is(err, ErrApproval) || !reflect.DeepEqual(a, Approval{}) {
			t.Errorf("%s owner accepted: %v", name, err)
		}
	}
	zero := p
	zero.DaemonUID, zero.DaemonGID = 0, 0
	if _, err := New(zero, b, Owner{}, testAssignment, reown(o, 0, 0)); !errors.Is(err, ErrApproval) {
		t.Error("a root daemon policy made root an owner", err)
	}
	a, err := New(p, b, testOwner, testAssignment, o)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(a.Validate(p, b, Owner{p.PayloadUID, p.PayloadGID}, testAssignment), ErrApproval) {
		t.Error("approval validated against a different expected owner")
	}
	for name, assignment := range map[string]Assignment{
		"legacy-project": {9999, 4 << 20, 4096},
		"partial-kib":    {10001, 4<<20 + 1, 4096},
		"tiny":           {10001, 512, 4096},
		"no-inodes":      {10001, 4 << 20, 0},
	} {
		if _, err := New(p, b, testOwner, assignment, o); !errors.Is(err, ErrApproval) {
			t.Errorf("%s assignment accepted: %v", name, err)
		}
	}
	for name, other := range map[string]Assignment{
		"project": {10002, 4 << 20, 4096}, "bytes": {10001, 8 << 20, 4096}, "inodes": {10001, 4 << 20, 4095},
	} {
		if !errors.Is(a.Validate(p, b, testOwner, other), ErrApproval) {
			t.Errorf("approval validated against another %s", name)
		}
	}
}

// The daemon worker must traverse, re-hash and unlink every approved entry, so a
// directory without owner rwx or a file without owner read is refused at plan time.
func TestApprovalRefusesEntriesTheDaemonWorkerCannotTraverseOrRead(t *testing.T) {
	for name, mutate := range map[string]func(*Observation){
		"directory-no-search": func(o *Observation) { o.Tree.Entries[0].Mode = uint32(os.ModeDir | 0600) },
		"directory-no-write":  func(o *Observation) { o.Tree.Entries[0].Mode = uint32(os.ModeDir | 0500) },
		"directory-no-read":   func(o *Observation) { o.Tree.Entries[0].Mode = uint32(os.ModeDir | 0300) },
		"file-unreadable":     func(o *Observation) { o.Tree.Entries[1].Mode = 0200 },
		"file-group-readable": func(o *Observation) { o.Tree.Entries[1].Mode = 0040 },
	} {
		t.Run(name, func(t *testing.T) { p, b, o := fixture(); mutate(&o); requireRefusal(t, p, b, o) })
	}
	for name, mutate := range map[string]func(*Observation){
		"read-only-file":     func(o *Observation) { o.Tree.Entries[1].Mode = 0400 },
		"group-dir-bits":     func(o *Observation) { o.Tree.Entries[0].Mode = uint32(os.ModeDir | 0750) },
		"symlink-unaffected": func(o *Observation) { o.Tree.Entries[2].Mode = uint32(os.ModeSymlink | 0777) },
	} {
		p, b, o := fixture()
		mutate(&o)
		if _, err := New(p, b, testOwner, testAssignment, o); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
}

// A retained approval observed before a reboot renumbered the data device parses
// against this boot's binding once rebased, and only then.
func TestRetainedApprovalRebasesOntoARenumberedDevice(t *testing.T) {
	p, b, o := fixture()
	a, err := New(p, b, testOwner, testAssignment, o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.Bytes(p, b, testOwner, testAssignment)
	if err != nil {
		t.Fatal(err)
	}
	rebooted := b
	rebooted.RootDevice = 44
	if _, err = Parse(raw, p, rebooted, testOwner, testAssignment); !errors.Is(err, ErrApproval) {
		t.Fatalf("an earlier boot's devices parsed against this boot: %v", err)
	}
	rebased, err := Rebase(raw, 44)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(rebased, p, rebooted, testOwner, testAssignment)
	if err != nil {
		t.Fatalf("rebased approval refused: %v", err)
	}
	if got.Observation.Tree.Root.Device != 44 || got.Observation.Parent.Device != 44 || got.Observation.Owners[3].Device != 44 || got.Observation.Tree.Entries[2].Inode != 13 {
		t.Fatalf("rebase changed more or less than the device: %+v", got.Observation)
	}
	if _, err = Parse(rebased, p, b, testOwner, testAssignment); !errors.Is(err, ErrApproval) {
		t.Fatalf("rebased approval still parsed against the old device: %v", err)
	}
	same, err := Rebase(raw, 8)
	if err != nil || !bytes.Equal(same, raw) {
		t.Fatalf("rebase onto the same device changed the approval: %v", err)
	}
	for name, bad := range map[string][]byte{
		"unknown field":   bytes.Replace(raw, []byte(`{"Version"`), []byte(`{"Extra":1,"Version"`), 1),
		"tampered inode":  bytes.Replace(raw, []byte(`"Inode":13`), []byte(`"Inode":14`), 1),
		"truncated":       raw[:len(raw)-1],
		"no device given": nil,
	} {
		device := uint64(44)
		if bad == nil {
			bad, device = raw, 0
		}
		if _, err = Rebase(bad, device); !errors.Is(err, ErrApproval) {
			t.Errorf("%s rebased: %v", name, err)
		}
	}
}
