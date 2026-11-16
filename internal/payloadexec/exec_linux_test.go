//go:build linux

package payloadexec

import (
	"errors"
	"os"
	"reflect"
	"syscall"
	"testing"
)

func TestOrdinaryCallerCannotEnterFixedHelper(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("ordinary helper refusal must run unprivileged")
	}
	uid, gid := os.Getuid(), os.Getgid()
	groups, err := syscall.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	environment := os.Environ()
	if err := Exec(); !errors.Is(err, ErrBoundary) {
		t.Fatalf("unprivileged helper caller: %v", err)
	}
	after, err := syscall.Getgroups()
	if err != nil || uid != os.Getuid() || gid != os.Getgid() || !reflect.DeepEqual(groups, after) || !reflect.DeepEqual(environment, os.Environ()) {
		t.Fatal("caller refusal changed credentials or environment")
	}
}
