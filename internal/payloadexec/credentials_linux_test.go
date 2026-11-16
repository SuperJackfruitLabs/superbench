//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"reflect"
	"syscall"
	"testing"
)

func TestCredentialDropRefusesOrdinaryCallerBeforeAnyChange(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("ordinary security tests must run unprivileged; privileged acceptance has a separate owned fixture")
	}
	groups, err := syscall.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Getuid(), os.Getgid()
	p, _ := layoutFixture(t, false)
	if err := dropCredentials(p); !errors.Is(err, errCredentials) {
		t.Fatalf("ordinary caller: %v", err)
	}
	after, err := syscall.Getgroups()
	if err != nil || os.Getuid() != uid || os.Getgid() != gid || !reflect.DeepEqual(groups, after) {
		t.Fatalf("refusal changed identity: %v", err)
	}
}
