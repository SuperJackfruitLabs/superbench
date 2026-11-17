//go:build linux

package freshrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

const uuid = "11111111-2222-3333-4444-555555555555"

func plans(t *testing.T) (payloadbootstrap.Spec, hoststorage.Policy, LeafPlan, RepairPlan) {
	t.Helper()
	p := hoststorage.Policy{OperationJournal: "indexed-v2", Root: "/srv/data", Ledger: "/var/lib/superbench/quota", OwnerUID: daemonUID, OwnerGID: daemonGID,
		BenchBytes: 32 << 20, StoreBytes: 16 << 20, MirrorBytes: 16 << 20, ReserveBytes: 16 << 20,
		BenchInodes: 4096, StoreInodes: 4096, MirrorInodes: 4096, ReserveInodes: 4096}
	s := testSpec("api")
	s.QuotaBytes = 16 << 20
	raw, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	authority := strings.Repeat("e", 64)
	b := payloadbootstrap.Inspection{Version: payloadbootstrap.InspectionVersion, Key: s.Key, ID: s.ID, SpecSHA256: digest(raw), IntentSHA256: strings.Repeat("c", 64),
		PolicyDigest: strings.Repeat("d", 64), Phase: "reserved", OperationDenied: true,
		Operation: quotaoperation.Identity{OperationID: strings.Repeat("1", 64), Epoch: strings.Repeat("2", 32), StateIdentity: strings.Repeat("3", 64), Authority: authority, BinarySHA256: strings.Repeat("4", 64)}}
	a := freshretirement.Approval{Version: freshretirement.Version, Key: s.Key, Authority: authority,
		Assignment: hoststorage.Assignment{Path: "/srv/data/benches/card", ProjectID: assigned, Bytes: 16 << 20, Inodes: 4096},
		Parent:     freshretirement.Parent{Device: device, Inode: 2, Mode: uint32(os.ModeDir | 0o755)}}
	leaf := LeafPlan{LeafVersion, b, a, Leaf{Node: dirNode(77, 0, 0)}}
	repair := RepairPlan{RepairVersion, b, a, uuid, Limit{assigned, 16 << 10, 4096}}
	return s, p, leaf, repair
}

// allocating returns the leaf plan for an allocating (b) leak at its recorded root.
func allocating(l LeafPlan, s payloadbootstrap.Spec) LeafPlan {
	l.Bootstrap.Phase = "allocating"
	l.Bootstrap.RecordedAllocation = l.Storage.Assignment
	l.Bootstrap.RecordedRoot.Device, l.Bootstrap.RecordedRoot.Inode = device, 50
	l.Leaf = Leaf{Node: dirNode(50, 0, 0), ProjectID: assigned, Fixed: fixedEntries(payloadbootstrap.FixedDirectories(s), 3)}
	return l
}

func TestPlansBindProvenanceStorageAndPhase(t *testing.T) {
	s, p, leaf, repair := plans(t)
	if err := leaf.validate(s, p); err != nil {
		t.Fatal("reserved leaf plan refused", err)
	}
	if err := allocating(leaf, s).validate(s, p); err != nil {
		t.Fatal("allocating (b) leaf plan refused", err)
	}
	if err := repair.validate(s, p); err != nil {
		t.Fatal("repair plan refused", err)
	}
	defaulted := s
	defaulted.QuotaBytes = 0
	if err := leaf.validate(defaulted, p); err == nil {
		t.Fatal("a defaulted quota matched an explicit one")
	}
	shared := map[string]func(*payloadbootstrap.Inspection, *freshretirement.Approval){
		"inspection version": func(b *payloadbootstrap.Inspection, _ *freshretirement.Approval) { b.Version = "v0" },
		"not denied":         func(b *payloadbootstrap.Inspection, _ *freshretirement.Approval) { b.OperationDenied = false },
		"other key":          func(b *payloadbootstrap.Inspection, _ *freshretirement.Approval) { b.Key = "other" },
		"other bid":          func(b *payloadbootstrap.Inspection, _ *freshretirement.Approval) { b.ID = strings.Repeat("f", 32) },
		"other spec": func(b *payloadbootstrap.Inspection, _ *freshretirement.Approval) {
			b.SpecSHA256 = strings.Repeat("f", 64)
		},
		"short intent hash": func(b *payloadbootstrap.Inspection, _ *freshretirement.Approval) { b.IntentSHA256 = "c" },
		"recorded root":     func(b *payloadbootstrap.Inspection, _ *freshretirement.Approval) { b.RecordedRoot.Inode = 50 },
		"recorded quota": func(b *payloadbootstrap.Inspection, a *freshretirement.Approval) {
			b.RecordedAllocation = a.Assignment
		},
		"other authority": func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) {
			a.Authority = strings.Repeat("f", 64)
		},
		"approval version": func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) { a.Version = "v0" },
		"approval key":     func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) { a.Key = "other" },
		"other caps":       func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) { a.Assignment.Bytes = 8 << 20 },
		"other path": func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) {
			a.Assignment.Path = "/srv/data/benches/other"
		},
		"legacy project": func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) { a.Assignment.ProjectID = 101 },
		"writable parent": func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) {
			a.Parent.Mode = uint32(os.ModeDir | 0o777)
		},
		"foreign parent":    func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) { a.Parent.UID = daemonUID },
		"other inode quota": func(_ *payloadbootstrap.Inspection, a *freshretirement.Approval) { a.Assignment.Inodes = 7 },
	}
	for name, mutate := range shared {
		l, r := leaf, repair
		mutate(&l.Bootstrap, &l.Storage)
		mutate(&r.Bootstrap, &r.Storage)
		if err := l.validate(s, p); !errors.Is(err, ErrRecovery) {
			t.Errorf("leaf plan with %s admitted: %v", name, err)
		}
		if err := r.validate(s, p); !errors.Is(err, ErrRecovery) {
			t.Errorf("repair plan with %s admitted: %v", name, err)
		}
	}
	moved := allocating(leaf, s)
	moved.Bootstrap.RecordedAllocation.Inodes++
	if err := moved.validate(s, p); !errors.Is(err, ErrRecovery) {
		t.Error("allocating plan with a different recorded allocation admitted", err)
	}
	for name, mutate := range map[string]func(*RepairPlan){
		"leaf version": func(r *RepairPlan) { r.Version = LeafVersion },
		"allocating phase": func(r *RepairPlan) {
			*r = RepairPlan{r.Version, allocating(leaf, s).Bootstrap, r.Storage, r.FilesystemUUID, r.Limit}
		},
		"staged phase":       func(r *RepairPlan) { r.Bootstrap.Phase = "staged" },
		"no uuid":            func(r *RepairPlan) { r.FilesystemUUID = "" },
		"other block cap":    func(r *RepairPlan) { r.Limit.BlockHardKiB++ },
		"other inode cap":    func(r *RepairPlan) { r.Limit.InodeHard = 0 },
		"other project":      func(r *RepairPlan) { r.Limit.ProjectID++ },
		"zero limit command": func(r *RepairPlan) { r.Limit = Limit{} },
	} {
		r := repair
		mutate(&r)
		if err := r.validate(s, p); !errors.Is(err, ErrRecovery) {
			t.Errorf("repair plan with %s admitted: %v", name, err)
		}
	}
	for name, mutate := range map[string]func(*LeafPlan){
		"repair version":  func(l *LeafPlan) { l.Version = RepairVersion },
		"foreign project": func(l *LeafPlan) { l.Leaf.ProjectID = assigned + 1 },
		"daemon leaf":     func(l *LeafPlan) { l.Leaf.Node.UID, l.Leaf.Node.GID = daemonUID, daemonGID },
		"other device":    func(l *LeafPlan) { l.Storage.Parent.Device++ },
	} {
		l := leaf
		mutate(&l)
		if err := l.validate(s, p); !errors.Is(err, ErrRecovery) {
			t.Errorf("leaf plan with %s admitted: %v", name, err)
		}
	}
}

func request(t *testing.T, s payloadbootstrap.Spec, plan any) []byte {
	t.Helper()
	raw, err := json.Marshal(struct {
		Spec payloadbootstrap.Spec
		Plan any
	}{s, plan})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestReviewedRequestsAreCanonicalAndBounded(t *testing.T) {
	s, _, leaf, repair := plans(t)
	leaf = allocating(leaf, s)
	raw := request(t, s, leaf)
	gotSpec, gotLeaf, err := ParseLeaf(raw)
	again, _ := json.Marshal(gotLeaf)
	want, _ := json.Marshal(leaf)
	if err != nil || gotSpec.Key != s.Key || string(again) != string(want) {
		t.Fatalf("canonical leaf request refused: %v", err)
	}
	if _, got, err := ParseRepair(request(t, s, repair)); err != nil || got != repair {
		t.Fatalf("canonical repair request refused: %+v %v", got, err)
	}
	other := s
	other.Key = "other"
	planText := string(raw[strings.Index(string(raw), `,"Plan":`)+1 : len(raw)-1])
	specText := string(raw[1:strings.Index(string(raw), `,"Plan":`)])
	for name, bad := range map[string][]byte{
		"unknown field":   []byte(strings.Replace(string(raw), `"Leaf":{`, `"Leaf":{"Extra":1,`, 1)),
		"trailing field":  []byte(string(raw[:len(raw)-1]) + `,"Extra":1}`),
		"reordered":       []byte("{" + planText + "," + specText + "}"),
		"duplicate plan":  []byte("{" + specText + "," + planText + "," + planText + "}"),
		"other key":       request(t, other, leaf),
		"invalid spec":    request(t, payloadbootstrap.Spec{Key: "card"}, leaf),
		"repair as leaf":  request(t, s, repair),
		"truncated":       raw[:len(raw)-2],
		"not utf8":        append(append([]byte{}, raw[:len(raw)-1]...), []byte(` `+"\xff"+`}`)...),
		"oversize":        []byte(strings.Replace(string(raw), `"Leaf":{`, `"Leaf":{`+strings.Repeat(" ", MaxRequest), 1)),
		"lowercase field": []byte(strings.Replace(string(raw), `"ProjectID"`, `"projectid"`, 1)),
	} {
		if _, _, err := ParseLeaf(bad); !errors.Is(err, ErrRecovery) {
			t.Errorf("%s leaf request admitted: %v", name, err)
		}
	}
	if _, _, err := ParseRepair(raw); !errors.Is(err, ErrRecovery) {
		t.Error("a leaf request parsed as a repair", err)
	}
	huge := leaf
	for len(huge.Leaf.Fixed) < 2000 {
		huge.Leaf.Fixed = append(huge.Leaf.Fixed, leaf.Leaf.Fixed...)
	}
	if _, err := encode(huge); !errors.Is(err, ErrRecovery) {
		t.Error("an oversize plan was encoded", err)
	}
}

func TestStepsRefuseWithoutADirectRootManager(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("ordinary test requires an unprivileged user")
	}
	s, _, leaf, repair := plans(t)
	ctx := context.Background()
	for _, m := range []*payloadbootstrap.Manager{nil, {}} {
		if _, err := PlanLeaf(ctx, m, s); err == nil {
			t.Error("leaf plan without a manager")
		}
		if _, err := RemoveLeaf(ctx, m, s, leaf); err == nil || strings.Contains(err.Error(), "effects may have occurred") {
			t.Errorf("leaf removal without a manager: %v", err)
		}
		if _, err := PlanRepair(ctx, m, s); err == nil {
			t.Error("repair plan without a manager")
		}
		if _, err := Repair(ctx, m, s, repair); err == nil || strings.Contains(err.Error(), "effects may have occurred") {
			t.Errorf("repair without a manager: %v", err)
		}
		if _, err := PlanClean(ctx, m, s); err == nil {
			t.Error("cleanup plan without a manager")
		}
		if _, err := Clean(ctx, m, s, strings.Repeat("a", 64)); err == nil || strings.Contains(err.Error(), "effects may have occurred") {
			t.Errorf("cleanup without a manager: %v", err)
		}
	}
}
