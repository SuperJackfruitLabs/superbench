package payloadbootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

func resultFixture(t *testing.T) (payloadexec.Policy, Spec, Result) {
	t.Helper()
	p := payloadexec.Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/srv/superbench-data", StateRoot: "/var/lib/superbench", CgroupRoot: "/sys/fs/cgroup/superbench"}
	raw, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	s := validSpecForTest()
	s.QuotaBytes = 8 << 20
	l := payloadexec.Layout{Version: payloadexec.LayoutVersion, Key: s.Key, ID: s.ID, PolicyDigest: hex.EncodeToString(sum[:]), Root: payloadexec.Node{Device: 8, Inode: 1}, RootHome: payloadexec.Node{Device: 8, Inode: 2}, RootTemp: payloadexec.Node{Device: 8, Inode: 3}, Members: []payloadexec.LayoutMember{{Name: "api", Checkout: payloadexec.Node{Device: 8, Inode: 4}, Admin: payloadexec.Node{Device: 8, Inode: 5}, Home: payloadexec.Node{Device: 8, Inode: 6}, Temp: payloadexec.Node{Device: 8, Inode: 7}}}}
	return p, s, Result{Version: ResultVersion, Layout: l, Allocation: quotahelper.Assignment{Path: p.DataRoot + "/benches/card", ProjectID: 10000, Bytes: s.QuotaBytes, Inodes: 4096}}
}

func TestBootstrapResultBindsOriginalAllocationAndGroupedRegistration(t *testing.T) {
	p, s, valid := resultFixture(t)
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseResult(raw, p, s)
	if err != nil || got.Allocation != valid.Allocation || got.Layout.ID != s.ID {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for name, change := range map[string]func(*Result){
		"version":        func(r *Result) { r.Version = "layout" },
		"bid":            func(r *Result) { r.Layout.ID = strings.Repeat("f", 32) },
		"key":            func(r *Result) { r.Layout.Key = "other" },
		"member":         func(r *Result) { r.Layout.Members[0].Name = "web" },
		"scalar":         func(r *Result) { r.Layout.Scalar = true },
		"policy":         func(r *Result) { r.Layout.PolicyDigest = strings.Repeat("e", 64) },
		"path":           func(r *Result) { r.Allocation.Path = p.DataRoot + "/benches/other" },
		"shared-project": func(r *Result) { r.Allocation.ProjectID = 100 },
		"request-cap":    func(r *Result) { r.Allocation.Bytes *= 2 },
		"zero-inodes":    func(r *Result) { r.Allocation.Inodes = 0 },
		"inode-overflow": func(r *Result) { r.Allocation.Inodes = 1 << 41 },
	} {
		t.Run(name, func(t *testing.T) {
			r := valid
			r.Layout.Members = append([]payloadexec.LayoutMember(nil), valid.Layout.Members...)
			change(&r)
			b, e := json.Marshal(r)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = ParseResult(b, p, s); !errors.Is(e, ErrBootstrap) {
				t.Fatalf("bad allocation accepted: %v", e)
			}
		})
	}
	for _, bad := range [][]byte{
		[]byte(strings.Replace(string(raw), `"Version":`, `"Unknown":0,"Version":`, 1)),
		[]byte(strings.Replace(string(raw), `"Allocation":`, `"Allocation":null,"Allocation":`, 1)),
		append(append([]byte{}, raw...), []byte(` {}`)...),
		[]byte(`null`),
		make([]byte, quotahelper.MaxReply+1),
	} {
		if _, e := ParseResult(bad, p, s); !errors.Is(e, ErrBootstrap) {
			t.Fatalf("noncanonical result accepted: %v", e)
		}
	}
}
