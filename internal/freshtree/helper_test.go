package freshtree

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection/layout"
)

type cannedReply struct {
	policy payloadpolicy.Policy
	digest string
	bench  *model.Bench
	value  registeredinspection.Preparation
}

func canned(t *testing.T) cannedReply {
	t.Helper()
	p := payloadpolicy.Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/sys/fs/cgroup/payload"}
	digest, err := layout.PolicyDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	n := func(i uint64) registeredinspection.Node { return registeredinspection.Node{Device: 1, Inode: i} }
	l := registeredinspection.Layout{Version: layout.LayoutVersion, Key: "card", ID: id, PolicyDigest: digest, Root: n(1), RootHome: n(2), RootTemp: n(3),
		Members: []layout.LayoutMember{{Name: "api", Checkout: n(4), Admin: n(5), Home: n(6), Temp: n(7)}, {Name: "web", Checkout: n(8), Admin: n(9), Home: n(10), Temp: n(11)}}}
	in := registeredinspection.Inputs{Version: registeredinspection.InputsVersion, Member: "web", Config: registeredinspection.InputFile{Name: ".bench/config.toml"},
		Manifests: []registeredinspection.InputFile{{Name: "package.json"}, {Name: "pnpm-lock.yaml"}, {Name: "package-lock.json"}}}
	b := &model.Bench{Key: "card", Path: "/data/benches/card", Workspace: &model.Workspace{ID: id, Members: []model.WorkspaceMember{{Name: "api"}, {Name: "web"}}}}
	return cannedReply{policy: p, digest: digest, bench: b, value: registeredinspection.Preparation{Version: registeredinspection.PreparationVersion, Layout: l, Inputs: in}}
}

func (c cannedReply) frame(t *testing.T, v registeredinspection.Preparation) []byte {
	t.Helper()
	raw, err := v.Bytes(c.policy)
	if err != nil {
		t.Fatal(err)
	}
	framed := make([]byte, 4+len(raw))
	binary.BigEndian.PutUint32(framed, uint32(len(raw)))
	copy(framed[4:], raw)
	return framed
}

func TestHelperReplyBindsTheExactWorkspace(t *testing.T) {
	c := canned(t)
	reply := c.frame(t, c.value)
	if v, err := bindPreparation(c.policy, c.digest, c.bench, "web", reply); err != nil || v.Inputs.Member != "web" {
		t.Fatalf("exact reply refused: %v", err)
	}
	lying := append([]byte(nil), reply...)
	binary.BigEndian.PutUint32(lying, uint32(len(reply))) // claims one byte more than it carries
	other := func(change func(*model.Bench)) *model.Bench {
		b := *c.bench
		w := *b.Workspace
		w.Members = append([]model.WorkspaceMember(nil), w.Members...)
		b.Workspace = &w
		change(&b)
		return &b
	}
	for name, tc := range map[string]struct {
		b      *model.Bench
		member string
		digest string
		reply  []byte
	}{
		"key":           {other(func(b *model.Bench) { b.Key = "other" }), "web", c.digest, reply},
		"layout ID":     {other(func(b *model.Bench) { b.Workspace.ID = strings.Repeat("b", 32) }), "web", c.digest, reply},
		"policy digest": {c.bench, "web", strings.Repeat("0", 64), reply},
		"member":        {c.bench, "api", c.digest, reply},
		"member order":  {other(func(b *model.Bench) { b.Workspace.Members[0].Name, b.Workspace.Members[1].Name = "web", "api" }), "web", c.digest, reply},
		"member count":  {other(func(b *model.Bench) { b.Workspace.Members = b.Workspace.Members[:1] }), "web", c.digest, reply},
		"frame length":  {c.bench, "web", c.digest, lying},
		"short frame":   {c.bench, "web", c.digest, reply[:3]},
	} {
		if _, err := bindPreparation(c.policy, tc.digest, tc.b, tc.member, tc.reply); !errors.Is(err, ErrHelper) {
			t.Fatalf("%s: mismatched reply accepted: %v", name, err)
		}
	}
}

func TestHelperRootRefusesUnregisteredEntriesOnlyWhenClean(t *testing.T) {
	v := canned(t).value
	if rootResult(v, true) != nil || rootResult(v, false) != nil {
		t.Fatal("registered-only root refused")
	}
	v.Unregistered = true
	if err := rootResult(v, true); !errors.Is(err, model.ErrDirty) {
		t.Fatalf("unregistered root entry was checkpointed: %v", err)
	}
	if err := rootResult(v, false); err != nil {
		t.Fatalf("preparation inspection refused a dirty root: %v", err)
	}
}
