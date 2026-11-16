package payloadexec

import (
	"errors"
	"reflect"
	"strings"
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

func TestAmbientExtractionPreservesPayloadEnvironmentAndValidation(t *testing.T) {
	ambient := []string{"HOME=private", "PATH=private", "GIT_CONFIG_COUNT=1", "LANG=C.UTF-8", "CI=true", "LC_ALL=C", "LD_PRELOAD=private"}
	locale, err := AmbientEnvironment(ambient)
	if err != nil {
		t.Fatal(err)
	}
	wantLocale := []string{"LANG=C.UTF-8", "CI=true", "LC_ALL=C"}
	if !reflect.DeepEqual(locale, wantLocale) {
		t.Fatalf("ambient allowlist changed: %q", locale)
	}
	prefix := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/payload/home", "TMPDIR=/payload/tmp", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	payload, err := Environment("/payload/home", "/payload/tmp", ambient)
	if err != nil || !reflect.DeepEqual(payload, append(prefix, wantLocale...)) {
		t.Fatalf("payload environment changed: %q %v", payload, err)
	}
	for _, invalid := range [][]string{{"LANG=C", "LANG=C"}, {"LANG"}, {"CI=" + strings.Repeat("x", 257)}, {"LC_ALL=\x00"}, {"LC_CTYPE=\xff"}} {
		if got, err := AmbientEnvironment(invalid); got != nil || !errors.Is(err, ErrPolicy) {
			t.Fatalf("ambient validation changed: %q %v", got, err)
		}
		if got, err := Environment("/payload/home", "/payload/tmp", invalid); got != nil || !errors.Is(err, ErrPolicy) {
			t.Fatalf("payload validation changed: %q %v", got, err)
		}
	}
	for _, paths := range [][2]string{{"/", "/"}, {"/payload/home", "/payload/home"}, {"relative", "/payload/tmp"}} {
		if got, err := Environment(paths[0], paths[1], ambient); got != nil || !errors.Is(err, ErrPolicy) {
			t.Fatalf("payload layout restriction relaxed: %q %v", got, err)
		}
	}
}
