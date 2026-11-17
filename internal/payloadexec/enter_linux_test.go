//go:build linux

package payloadexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// enterPayload is the one place the drop→descriptors→input→exec order lives.
// Without an actual drop to the payload identity it must refuse before it
// binds stdin, reports exec or execs anything.
func TestEnterPayloadRefusesBeforeInputOrExecWithoutDroppedCredentials(t *testing.T) {
	if os.Getenv("SB_ENTER_PAYLOAD_CHILD") == "1" {
		before, err := os.Stdin.Stat()
		if err != nil {
			t.Fatal(err)
		}
		r := launchRequest()
		r.Input, r.ReportExec, r.Arguments = []byte{0, 1, 2}, true, []string{"/bin/sh", "-c", "echo EXECUTED"}
		input, err := openPayloadInput(r)
		if err != nil {
			t.Fatal(err)
		}
		phase := ""
		p := Policy{DaemonUID: 61001, DaemonGID: 61003, PayloadUID: 61002, PayloadGID: 61004, DataRoot: "/data", StateRoot: "/state", CgroupRoot: "/cgroup"}
		err = enterPayload(&phase, p, r, input, "/bin/sh", []string{"PATH=/usr/bin:/bin"})
		after, statErr := os.Stdin.Stat()
		if !errors.Is(err, errCredentials) || phase != "credentials" || statErr != nil || !os.SameFile(before, after) {
			t.Fatalf("refusal order: phase=%s err=%v stdin replaced=%v", phase, err, statErr != nil || !os.SameFile(before, after))
		}
		_, _ = os.Stdout.WriteString("REFUSED\n")
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEnterPayloadRefusesBeforeInputOrExecWithoutDroppedCredentials$")
	cmd.Env = append(os.Environ(), "SB_ENTER_PAYLOAD_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.HasPrefix(out, []byte("REFUSED\n")) || bytes.Contains(out, []byte(ExecReached)) || bytes.Contains(out, []byte("EXECUTED")) {
		t.Fatalf("enterPayload ran without a credential drop: %v %q", err, out)
	}
}
