package payloadbootstrap

import (
	"crypto/sha256"
	"encoding/hex"
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
