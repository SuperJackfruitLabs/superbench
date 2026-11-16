//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func TestHelperPrivateAndPublicPolicyMustMatchEveryField(t *testing.T) {
	p := Policy{DaemonUID: 1001, DaemonGID: 1003, PayloadUID: 1002, PayloadGID: 1004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
	if err := matchHelperPolicy(p, p); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Policy){
		"daemon uid": func(p *Policy) { p.DaemonUID = 1005 }, "daemon gid": func(p *Policy) { p.DaemonGID = 1005 },
		"payload uid": func(p *Policy) { p.PayloadUID = 1005 }, "payload gid": func(p *Policy) { p.PayloadGID = 1005 },
		"data": func(p *Policy) { p.DataRoot = "/other-data" }, "state": func(p *Policy) { p.StateRoot = "/other-state" },
		"cgroup": func(p *Policy) { p.CgroupRoot = "/other-cgroup" }, "invalid": func(p *Policy) { p.PayloadUID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := p
			change(&changed)
			for _, pair := range [][2]Policy{{p, changed}, {changed, p}} {
				err := matchHelperPolicy(pair[0], pair[1])
				if !errors.Is(err, ErrBoundary) || !errors.Is(err, serviceidentity.ErrAuthority) {
					t.Fatalf("mismatched policy granted: %v", err)
				}
			}
		})
	}
	if err := matchHelperPolicy(Policy{}, Policy{}); !errors.Is(err, serviceidentity.ErrAuthority) {
		t.Fatalf("zero policies matched: %v", err)
	}
}

func TestOrdinaryCallerCannotLoadOrRecheckHelperBinding(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("helper binding caller refusal must run unprivileged")
	}
	binding, err := loadHelperBinding()
	if binding != (helperBinding{}) || !errors.Is(err, ErrBoundary) {
		t.Fatalf("ordinary helper binding: %#v %v", binding, err)
	}
	if err := (helperBinding{}).recheck(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("ordinary binding recheck: %v", err)
	}
}
