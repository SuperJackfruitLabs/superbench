package payloadexec

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func layoutFixture(t *testing.T, scalar bool) (Policy, Layout) {
	t.Helper()
	p := Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/srv/superbench-data", StateRoot: "/var/lib/superbench", CgroupRoot: "/sys/fs/cgroup/superbench"}
	digest, err := policyDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	node := Node{Device: 8, Inode: 42}
	name := "api"
	if scalar {
		name = ""
	}
	l := Layout{Version: LayoutVersion, Key: "card", ID: strings.Repeat("a", 32), PolicyDigest: digest, Scalar: scalar, Root: node, Members: []LayoutMember{{Name: name, Checkout: Node{8, 43}, Admin: Node{8, 44}, Home: Node{8, 45}, Temp: Node{8, 46}}}}
	if !scalar {
		l.RootHome, l.RootTemp = Node{8, 47}, Node{8, 48}
	}
	return p, l
}

func TestPrivateLayoutKeepsAdminAndRuntimeOutsideCheckoutInsideAllocation(t *testing.T) {
	for _, scalar := range []bool{false, true} {
		p, l := layoutFixture(t, scalar)
		name, part := "api", "api"
		if scalar {
			name, part = "", "worktree"
		}
		checkout, admin, home, tmp, err := l.paths(p, name)
		if err != nil || checkout != p.DataRoot+"/benches/card/"+part || admin != p.DataRoot+"/benches/card/.superbench-git/"+part || home != p.DataRoot+"/benches/card/.superbench-runtime/"+part+"/home" || tmp != p.DataRoot+"/benches/card/.superbench-runtime/"+part+"/tmp" {
			t.Fatalf("invalid private paths: %q %q %q %q %v", checkout, admin, home, tmp, err)
		}
		if _, _, _, _, err := l.paths(p, "../other"); !errors.Is(err, ErrPolicy) {
			t.Fatalf("unregistered member: %v", err)
		}
	}
}

func TestLargestValidLayoutFitsEnvelopeAndBindsPolicy(t *testing.T) {
	p, l := layoutFixture(t, false)
	l.Root = Node{Device: math.MaxUint64, Inode: math.MaxUint64}
	l.RootHome, l.RootTemp = Node{math.MaxUint64, math.MaxUint64 - 200}, Node{math.MaxUint64, math.MaxUint64 - 201}
	l.Members = nil
	for i := 0; i < 32; i++ {
		name := string(rune('A'+i)) + strings.Repeat("x", 79)
		// Keep every name within the actual ASCII logical-name alphabet.
		if i >= 26 {
			name = string(rune('a'+i-26)) + strings.Repeat("x", 79)
		}
		l.Members = append(l.Members, LayoutMember{Name: name, Checkout: Node{math.MaxUint64, math.MaxUint64 - uint64(4*i+1)}, Admin: Node{math.MaxUint64, math.MaxUint64 - uint64(4*i+2)}, Home: Node{math.MaxUint64, math.MaxUint64 - uint64(4*i+3)}, Temp: Node{math.MaxUint64, math.MaxUint64 - uint64(4*i+4)}})
	}
	raw, err := l.Bytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLayout(p, raw); err != nil {
		t.Fatal(err)
	}
	p.PayloadUID++
	if _, err := ParseLayout(p, raw); !errors.Is(err, ErrPolicy) {
		t.Fatalf("changed fixed identity accepted: %v", err)
	}
}

func TestLayoutRejectsAliasedRootOrMemberRoles(t *testing.T) {
	p, l := layoutFixture(t, false)
	l.Members[0].Home = l.Root
	if _, err := l.Bytes(p); !errors.Is(err, ErrPolicy) {
		t.Fatalf("root reused as HOME: %v", err)
	}
	p, l = layoutFixture(t, false)
	l.Members[0].Home = l.Members[0].Admin
	if _, err := l.Bytes(p); !errors.Is(err, ErrPolicy) {
		t.Fatalf("Git admin reused as HOME: %v", err)
	}
}
