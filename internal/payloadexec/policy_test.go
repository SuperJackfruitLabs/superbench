package payloadexec

import (
	"errors"
	"testing"
)

func TestManagedPolicyRequiresDistinctUnprivilegedPayloadIdentity(t *testing.T) {
	valid := Policy{DaemonUID: 1001, DaemonGID: 1001, StateRoot: "/var/lib/superbench", PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/srv/superbench-data", CgroupRoot: "/sys/fs/cgroup/superbench"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Policy){
		func(p *Policy) { p.PayloadUID = 0 },
		func(p *Policy) { p.PayloadGID = 0 },
		func(p *Policy) { p.DaemonUID = 0 },
		func(p *Policy) { p.PayloadUID = p.DaemonUID },
		func(p *Policy) { p.PayloadGID = p.DaemonGID },
		func(p *Policy) { p.StateRoot = p.DataRoot + "/state" },
		func(p *Policy) { p.DataRoot = p.StateRoot + "/data" },
		func(p *Policy) { p.DataRoot = "/" },
		func(p *Policy) { p.DataRoot = "/srv/../data" },
		func(p *Policy) { p.CgroupRoot = "relative" },
	} {
		candidate := valid
		change(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrPolicy) {
			t.Fatalf("policy %#v: got %v, want ErrPolicy", candidate, err)
		}
	}
}

func TestPayloadEnvironmentDoesNotInheritDaemonOrLoaderAuthority(t *testing.T) {
	env, err := Environment("/srv/superbench-data/store/payload-home", "/srv/superbench-data/store/payload-tmp", []string{
		"PATH=/tmp/malicious", "HOME=/var/lib/superbench", "TMPDIR=/var/lib/superbench",
		"BENCH_LEASE_TOKEN=secret", "AWS_SECRET_ACCESS_KEY=secret", "DATABASE_URL=secret",
		"LD_PRELOAD=/tmp/code.so", "LD_LIBRARY_PATH=/tmp", "GIT_SSH_COMMAND=secret",
		"SUDO_UID=0", "PYTHONPATH=/tmp", "BASH_ENV=/tmp/code", "ENV=/tmp/code", "NODE_OPTIONS=--require=/tmp/code",
		"LANG=C.UTF-8", "CI=true",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/srv/superbench-data/store/payload-home", "TMPDIR=/srv/superbench-data/store/payload-tmp", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LANG=C.UTF-8", "CI=true"}
	if len(env) != len(want) {
		t.Fatalf("unexpected environment: %q", env)
	}
	for i := range want {
		if env[i] != want[i] {
			t.Fatalf("environment %d = %q, want %q", i, env[i], want[i])
		}
	}
}
