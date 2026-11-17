//go:build linux && serviceidentityfixture

package payloadexec

// Opt-in root fixture for the helper's finite-input sequence: the sealed memfd
// is created while root, the actual dropCredentials runs, then Exec's own
// enterPayload binds stdin once, reports exec and execs an ordinary reader as
// the payload identity. Not a sudo, scope, layout or service-lifecycle claim.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadinput"
)

var finiteFixturePolicy = Policy{DaemonUID: 61001, DaemonGID: 61003, PayloadUID: 61002, PayloadGID: 61004, DataRoot: "/var/tmp/superbench-finite-input-data", StateRoot: "/var/tmp/superbench-finite-input-state", CgroupRoot: "/var/tmp/superbench-finite-input-cgroup"}

// More than a pipe buffer, so a delivery that was not one sealed file blocks.
func finiteFixtureInput() []byte { return bytes.Repeat([]byte{0, 0xff, 'x', '\n'}, 70000) }

var finiteFixtureMarkers = map[string][]string{
	"sealed": {"FINITE_INPUT_SEALED_DELIVERED_AFTER_CREDENTIAL_DROP", "FINITE_INPUT_IMMUTABLE_READ_ONLY_REFUSALS", "FINITE_INPUT_EXACT_BYTES_EOF_NO_DESCRIPTOR_LEAK"},
	"empty":  {"FINITE_INPUT_EMPTY_KEEPS_INHERITED_STDIN"},
}

func TestFiniteInputSealedAfterCredentialDropFixture(t *testing.T) {
	helperFixtureNamespaceGuard(t) // Before any child, descriptor or credential change.
	for _, mode := range []string{"sealed", "empty"} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFiniteInputDeliveryChild$", "-test.timeout=50s")
		cmd.Env = append(os.Environ(), "SUPERBENCH_FINITE_INPUT_CHILD="+mode)
		cmd.Dir = "/"
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s delivery child: %v\n%s", mode, err, out)
		}
		if (mode == "sealed") != bytes.HasPrefix(out, []byte(ExecReached)) {
			t.Fatalf("%s reader exec report: %q", mode, out[:min(len(out), 64)])
		}
		for _, marker := range finiteFixtureMarkers[mode] {
			if !bytes.Contains(out, []byte("READER "+marker+"\n")) {
				t.Fatalf("%s reader did not prove %s:\n%s", mode, marker, out)
			}
			t.Log(marker)
		}
	}
}

// Root child: Exec's open and drop, then the shared enterPayload into the reader.
func TestFiniteInputDeliveryChild(t *testing.T) {
	mode := os.Getenv("SUPERBENCH_FINITE_INPUT_CHILD")
	if mode == "" {
		return
	}
	helperFixtureNamespaceGuard(t)
	r := launchRequest()
	r.Arguments = []string{os.Args[0], "-test.run=^TestFiniteInputReaderChild$", "-test.timeout=30s"}
	if mode == "sealed" {
		r.Input, r.ReportExec = finiteFixtureInput(), true
	}
	input, err := openPayloadInput(r)
	if err != nil || (input == nil) != (mode == "empty") {
		t.Fatalf("open: %v", err)
	}
	if err := dropCredentials(finiteFixturePolicy); err != nil {
		t.Fatalf("drop: %v", err)
	}
	// The sealed descriptor itself is close-on-exec; only its stdin duplicate survives.
	phase := "fixture"
	env := []string{"PATH=/usr/bin:/bin", "SUPERBENCH_FINITE_INPUT_READER=" + mode}
	t.Fatalf("%s: %v", phase, enterPayload(&phase, finiteFixturePolicy, r, input, os.Args[0], env))
}

// Payload-identity reader: proves who it is and what its stdin is and allows.
func TestFiniteInputReaderChild(t *testing.T) {
	mode := os.Getenv("SUPERBENCH_FINITE_INPUT_READER")
	if mode == "" {
		return
	}
	if err := verifyDropped(finiteFixturePolicy); err != nil {
		t.Fatalf("reader still holds daemon or root authority: %v", err)
	}
	say := func(marker string) { _, _ = os.Stdout.WriteString("READER " + marker + "\n") }
	if mode == "empty" {
		st, err := os.Stdin.Stat()
		raw, readErr := io.ReadAll(os.Stdin)
		if err != nil || st.Mode()&os.ModeCharDevice == 0 || readErr != nil || len(raw) != 0 {
			t.Fatalf("empty input replaced inherited stdin: %v %v %d", err, readErr, len(raw))
		}
		say("FINITE_INPUT_EMPTY_KEEPS_INHERITED_STDIN")
		return
	}
	if err := payloadinput.Check(os.Stdin); err != nil {
		t.Fatalf("stdin is not the sealed read-only memfd at offset zero: %v", err)
	}
	say("FINITE_INPUT_SEALED_DELIVERED_AFTER_CREDENTIAL_DROP")
	if _, err := syscall.Write(0, []byte{1}); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("stdin writable: %v", err)
	}
	if err := syscall.Ftruncate(0, 0); err == nil {
		t.Fatal("stdin truncatable")
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, 0, 1033, 0); errno != syscall.EPERM {
		t.Fatalf("seal set still changeable: %v", errno)
	}
	if f, err := os.OpenFile("/proc/self/fd/0", os.O_RDWR, 0); err == nil {
		_ = f.Close()
		t.Fatal("payload identity reopened its input writable")
	}
	say("FINITE_INPUT_IMMUTABLE_READ_ONLY_REFUSALS")
	raw, err := io.ReadAll(os.Stdin)
	again, againErr := os.Stdin.Read(make([]byte, 1))
	want, got := sha256.Sum256(finiteFixtureInput()), sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(got[:]) != hex.EncodeToString(want[:]) || again != 0 || againErr != io.EOF {
		t.Fatalf("input bytes or EOF changed: %d %v %d %v", len(raw), err, again, againErr)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		fd, _ := strconv.Atoi(entry.Name())
		target, _ := os.Readlink("/proc/self/fd/" + entry.Name())
		if fd != 0 && strings.Contains(target, "memfd:") {
			t.Fatalf("extra input descriptor %d survived exec: %s", fd, target)
		}
	}
	say("FINITE_INPUT_EXACT_BYTES_EOF_NO_DESCRIPTOR_LEAK")
}
