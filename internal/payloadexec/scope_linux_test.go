//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestChangedPolicyRefusesBeforeLookingUpScope(t *testing.T) {
	p, _ := layoutFixture(t, false)
	p.CgroupRoot = filepath.Join(t.TempDir(), "not-created")
	r := launchRequest()
	r.PolicyDigest = strings.Repeat("f", 64)
	fd, err := VerifyScope(p, r)
	if fd != nil || !errors.Is(err, ErrPolicy) || !errors.Is(err, ErrScope) {
		t.Fatalf("changed policy must refuse before scope lookup: %v %v", fd, err)
	}
	if _, err := os.Lstat(p.CgroupRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("policy refusal created scope path: %v", err)
	}
}

func TestOrdinaryDirectoryCannotClaimToBeRecordedCgroup(t *testing.T) {
	root := t.TempDir()
	r := launchRequest()
	path := filepath.Join(root, r.Scope.ID)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(path, "cgroup.procs")
	bytes := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if err := os.WriteFile(fake, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	p := Policy{DaemonUID: 1001, DaemonGID: 1001, PayloadUID: 1002, PayloadGID: 1002, DataRoot: "/srv/superbench-data", StateRoot: "/var/lib/superbench", CgroupRoot: root}
	digest, err := policyDigest(p)
	if err != nil {
		t.Fatal(err)
	}
	r.PolicyDigest = digest
	fd, err := VerifyScope(p, r)
	if fd != nil || !errors.Is(err, ErrScope) {
		t.Fatalf("fake cgroup: %v %v", fd, err)
	}
	got, err := os.ReadFile(fake)
	if err != nil || string(got) != string(bytes) {
		t.Fatalf("read-only refusal changed fixture: %q %v", got, err)
	}
}
