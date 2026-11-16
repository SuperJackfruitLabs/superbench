//go:build linux

package quotaoperationcontrol

import (
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"os"
	"strings"
	"testing"
)

func TestFreshInstallGateRequiresActualStoppedUnitFields(t *testing.T) {
	valid := "LoadState=masked\nActiveState=inactive\nSubState=dead\nMainPID=0\nControlPID=0\nUnitFileState=masked\nJob=\nUser=superbench\nGroup=superbench\n"
	if err := stoppedUnit([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, legitimate := range []string{strings.Replace(strings.Replace(valid, "User=superbench\n", "", 1), "Group=superbench\n", "", 1), strings.Replace(strings.Replace(valid, "User=superbench", "User=", 1), "Group=superbench", "Group=", 1)} {
		if err := stoppedUnit([]byte(legitimate)); err != nil {
			t.Fatalf("legitimate unloaded masked unit refused: %q %v", legitimate, err)
		}
	}
	for _, bad := range []string{
		strings.Replace(valid, "LoadState=masked", "LoadState=loaded", 1),
		strings.Replace(valid, "ActiveState=inactive", "ActiveState=active", 1),
		strings.Replace(valid, "MainPID=0", "MainPID=123", 1),
		strings.Replace(valid, "ControlPID=0", "ControlPID=123", 1),
		strings.Replace(valid, "UnitFileState=masked", "UnitFileState=masked-runtime", 1),
		strings.Replace(valid, "Job=", "Job=42", 1),
		strings.Replace(valid, "Job=\n", "", 1), valid + "Job=\n",
		strings.Replace(valid, "Job=", "Unknown=", 1),
		strings.Replace(valid, "User=superbench", "User=root", 1),
		strings.Replace(valid, "Group=superbench", "Group=other", 1),
	} {
		if err := stoppedUnit([]byte(bad)); !errors.Is(err, ErrInstall) {
			t.Fatalf("stopped gate accepted %q: %v", bad, err)
		}
	}
}

func TestControllerInventoryRejectsEveryCredentialAndAmbiguity(t *testing.T) {
	if err := refuseIdentity([]byte("Name:\tfixture\nUid:\t42\t42\t42\t42\n"), 1001, 61001); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"Uid:\t1001 42 42 42\n", "Uid:\t42 61001 42 42\n", "Uid:\t42 42 1001 42\n", "Uid:\t42 42 42 61001\n", "Uid:\t42 42 42\n", "Uid:\t42 42 x 42\n", "Name: fixture\n", "Uid: 42 42 42 42\nUid: 42 42 42 42\n"} {
		if err := refuseIdentity([]byte(s), 1001, 61001); !errors.Is(err, ErrInstall) {
			t.Fatalf("inventory accepted %q: %v", s, err)
		}
	}
}

func TestInitializeFreshRefusesOrdinaryIdentityBeforeAnyPolicyRead(t *testing.T) {
	if os.Getuid() == 0 || os.Geteuid() == 0 {
		t.Fatal("ordinary fixture must be unprivileged")
	}
	c, p, err := InitializeFresh(context.Background())
	if !errors.Is(err, ErrInstall) || c != nil || p.Root != "" {
		t.Fatalf("ordinary initialization returned authority: %v %+v %v", c, p, err)
	}
}

func TestInstallInventoryRequiresUnfilteredWholeProcMount(t *testing.T) {
	valid := "42 1 0:4 / /proc rw,nosuid,nodev,noexec - proc proc rw\n"
	if err := fullProcMount([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", strings.Replace(valid, " / /proc ", " /subset /proc ", 1), strings.Replace(valid, "- proc proc rw", "- proc proc rw,hidepid=2", 1), strings.Replace(valid, "- proc proc rw", "- proc proc rw,subset=pid", 1), valid + valid, strings.Replace(valid, "- proc proc rw", "- tmpfs tmpfs rw", 1)} {
		if err := fullProcMount([]byte(bad)); !errors.Is(err, ErrInstall) {
			t.Fatalf("filtered proc accepted %q: %v", bad, err)
		}
	}
}

func TestFixedUnitAccountMustMatchInstalledDaemonIdentity(t *testing.T) {
	p := hoststorage.Policy{OwnerUID: 1001, OwnerGID: 1002}
	if err := fixedAccountIDs("1001", "1002", p); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][2]string{{"1003", "1002"}, {"1001", "1003"}, {"01001", "1002"}, {"0", "1002"}, {"1001", "0"}, {"", "1002"}, {"1001", "bad"}} {
		if err := fixedAccountIDs(ids[0], ids[1], p); !errors.Is(err, ErrInstall) {
			t.Fatalf("account mismatch accepted: %q %v", ids, err)
		}
	}
}
