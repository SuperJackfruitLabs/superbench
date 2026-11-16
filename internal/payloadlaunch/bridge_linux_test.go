//go:build linux

package payloadlaunch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
)

type fenceFunc func(*execution.Execution) error

func (f fenceFunc) Fence(e *execution.Execution) error { return f(e) }

var sinkFault = errors.New("fixture output writer fault")

type faultySink struct{}

func (faultySink) Write([]byte) (int, error) { return 0, sinkFault }
func scopeFixture() *execution.Execution {
	return &execution.Execution{ID: strings.Repeat("1", 32), Root: "/sys/fs/cgroup/fixture", HostID: strings.Repeat("2", 64), BootID: "00000000-0000-0000-0000-000000000001", Phase: "starting"}
}
func targetFixture(kind string) Target {
	return Target{Key: "fixture", LayoutID: strings.Repeat("3", 32), Member: "a", PolicyDigest: strings.Repeat("4", 64), Kind: kind}
}

// mockSubprocess substitutes ONLY this test's own executable after checking the
// product's fixed sudo invocation. It proves transport mechanics, never sudo,
// root credential drop, cgroup fencing or production registration authority.
func mockSubprocess(t *testing.T, cmd *exec.Cmd, role string) {
	t.Helper()
	if cmd.Path != "/usr/bin/sudo" || !reflect.DeepEqual(cmd.Args, []string{"sudo", "-n", "--", Helper}) || cmd.Dir != "/" || len(cmd.ExtraFiles) != 0 {
		t.Fatal("nonfixed helper invocation")
	}
	cmd.Path = os.Args[0]
	cmd.Args = []string{os.Args[0], "-test.run=^TestTransportOwnedSubprocess$"}
	cmd.Env = append(cmd.Env, "PAYLOAD_TRANSPORT_TEST_ROLE="+role)
}
func startBridge(t *testing.T, b *Bridge, role string) {
	t.Helper()
	mockSubprocess(t, b.cmd, role)
	if err := b.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.StartIO(); err != nil {
		_ = b.cmd.Process.Kill()
		_ = b.cmd.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = b.cmd.Process.Kill()
		_ = b.WaitLeader()
		_ = b.Close()
	})
}
func bounded(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func TestRegularLogSurvivesCallerCloseAndNeverBecomesChildStdio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("original-payload", "arg")
	cmd.Stdout = file
	cmd.Stderr = file
	cmd.Env = []string{"DAEMON_SECRET=fixture", "LD_PRELOAD=fixture", "LANG=C"}
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, v := range []any{cmd.Stdin, cmd.Stdout, cmd.Stderr} {
		f, ok := v.(*os.File)
		if !ok {
			t.Fatal("stdio is not explicit pipe")
		}
		s, e := f.Stat()
		if e != nil || s.Mode()&os.ModeNamedPipe == 0 {
			t.Fatal("regular/control descriptor supplied to helper")
		}
	}
	duplicate := b.pumps[0].sink.(*os.File)
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, duplicate.Fd(), syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("sink duplicate not atomically CLOEXEC")
	}
	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "DAEMON_SECRET=") || strings.HasPrefix(env, "LD_") {
			t.Fatal("ambient secret/loader retained")
		}
	}
	startBridge(t, b, "output")
	if err = file.Close(); err != nil {
		t.Fatal(err)
	} // Exact stageexec's immediate post-Start close.
	if err = b.WaitLeader(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := bounded(t)
	defer cancel()
	fenced := 0
	if err = b.Drain(ctx, fenceFunc(func(*execution.Execution) error { fenced++; return nil })); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "stdout\x00bytes\nstderr\n" || fenced != 1 {
		t.Fatalf("closed-caller log lost bytes: %q %v", got, err)
	}
}

func TestGitFrameAndBinaryInputAreExactAndOwned(t *testing.T) {
	cmd := exec.Command("git", gitpolicy.Arguments("hash-object", "--stdin")[1:]...)
	var out bytes.Buffer
	cmd.Stdout = &out
	data := []byte{0, 255, 10, 0, 13, 4}
	wantData := bytes.Clone(data)
	e := scopeFixture()
	oldScope := *e
	b, err := Prepare(cmd, targetFixture("git"), e, GitInput{Bytes: data, Limit: uint64(len(data))})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	data[0] = 99
	e.ID = strings.Repeat("f", 32)
	cmdErr := cmd.Err
	if cmdErr != nil {
		t.Fatal(cmdErr)
	}
	startBridge(t, b, "echo-frame")
	if err = b.WaitLeader(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := bounded(t)
	defer cancel()
	if err = b.Drain(ctx, fenceFunc(func(actual *execution.Execution) error {
		if !reflect.DeepEqual(*actual, oldScope) {
			t.Error("mutable execution substituted for pinned scope")
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	raw := out.Bytes()
	if len(raw) < 4 {
		t.Fatal("missing frame")
	}
	n := int(binary.BigEndian.Uint32(raw))
	if n > len(raw)-4 {
		t.Fatal("frame length")
	}
	req, err := payloadexec.ParseRequest(raw[4 : 4+n])
	if err != nil || req.Kind != "git" || req.Scope.ID != oldScope.ID || !reflect.DeepEqual(req.Arguments, gitpolicy.Arguments("hash-object", "--stdin")) || !bytes.Equal(raw[4+n:], wantData) {
		t.Fatalf("frame or Git bytes changed: %v", err)
	}
}

func TestSetsidHolderCannotDelayLeaderWaitOrPreventFence(t *testing.T) {
	cmd := exec.Command("payload")
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	holder := exec.Command(os.Args[0], "-test.run=^TestTransportOwnedSubprocess$")
	holder.Env = []string{"PAYLOAD_TRANSPORT_TEST_ROLE=hold"}
	holder.Stdout = cmd.Stdout
	holder.Stderr = cmd.Stderr
	holder.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = holder.Process.Kill()
		if holder.ProcessState == nil {
			_ = holder.Wait()
		}
	}()
	if sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, uintptr(holder.Process.Pid), 0, 0); errno != 0 || sid != uintptr(holder.Process.Pid) {
		t.Fatal("owned holder did not setsid")
	}
	startBridge(t, b, "output")
	leader := make(chan error, 1)
	go func() { leader <- b.WaitLeader() }()
	select {
	case err := <-leader:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leader Wait waited for descendant stdout before fence")
	}
	ctx, cancel := bounded(t)
	defer cancel()
	if err = b.Drain(ctx, fenceFunc(func(*execution.Execution) error {
		if err := holder.Process.Kill(); err != nil {
			return err
		}
		err := holder.Wait()
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return errors.New("owned holder not reaped")
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
}

func TestConsumedPumpNotificationCannotBecomeSuccessfulDrain(t *testing.T) {
	cmd := exec.Command("payload")
	cmd.Stdout = faultySink{}
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	startBridge(t, b, "output")
	select {
	case err := <-b.Failures():
		if !errors.Is(err, sinkFault) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pump failure not observable")
	}
	if err = b.WaitLeader(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := bounded(t)
	defer cancel()
	fenced, sealed := 0, false
	err = b.Drain(ctx, fenceFunc(func(*execution.Execution) error { fenced++; return nil }))
	if err == nil {
		sealed = true
	}
	if !errors.Is(err, sinkFault) || fenced != 1 || sealed {
		t.Fatalf("pump failure allowed success/seal after event consumption: %v", err)
	}
}

func TestFiniteGitInputFailureCannotSeal(t *testing.T) {
	cmd := exec.Command("git", gitpolicy.Arguments("fixture")[1:]...)
	input := bytes.Repeat([]byte{1}, 1<<20)
	b, err := Prepare(cmd, targetFixture("git"), scopeFixture(), GitInput{Bytes: input, Limit: uint64(len(input))})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	startBridge(t, b, "close-input")
	if err = b.WaitLeader(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := bounded(t)
	defer cancel()
	fenced := false
	err = b.Drain(ctx, fenceFunc(func(*execution.Execution) error { fenced = true; return nil }))
	if !errors.Is(err, syscall.EPIPE) || !fenced {
		t.Fatalf("input failure lost or unfenced: %v", err)
	}
}

func TestOriginalContextCancelAndWaitDelaySurvivePreparation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "payload")
	cmd.WaitDelay = 37 * time.Millisecond
	var cancels atomic.Int32
	cmd.Cancel = func() error { cancels.Add(1); return cmd.Process.Kill() }
	originalCancel := reflect.ValueOf(cmd.Cancel).Pointer()
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if cmd.WaitDelay != 37*time.Millisecond || reflect.ValueOf(cmd.Cancel).Pointer() != originalCancel {
		t.Fatal("original Cmd cancellation replaced")
	}
	startBridge(t, b, "wait")
	cancel()
	err = b.WaitLeader()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || cancels.Load() != 1 {
		t.Fatalf("original context was detached: %v, cancels%d", err, cancels.Load())
	}
	drainCtx, stop := bounded(t)
	defer stop()
	if err = b.Drain(drainCtx, fenceFunc(func(*execution.Execution) error { return nil })); err != nil && !errors.Is(err, syscall.EPIPE) {
		t.Fatal(err)
	}
}

func TestRejectedInputAndStartFailurePreserveCallerResources(t *testing.T) {
	for _, input := range []GitInput{{Bytes: []byte{1}, Limit: 1}, {Limit: 1}} {
		cmd := exec.Command("payload")
		if _, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), input); !errors.Is(err, ErrTransport) {
			t.Fatal("payload adopted stdin")
		}
	}
	cmd := exec.Command("git")
	if _, err := Prepare(cmd, targetFixture("git"), scopeFixture(), GitInput{Bytes: []byte{1, 2}, Limit: 1}); !errors.Is(err, ErrTransport) {
		t.Fatal("Git exceeded approved input bound")
	}
	var sink bytes.Buffer
	cmd = exec.Command("payload")
	cmd.Stdout = &sink
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx, cancel := bounded(t)
	defer cancel()
	if err = b.Drain(ctx, fenceFunc(func(*execution.Execution) error { calls++; return nil })); !errors.Is(err, ErrTransport) || calls != 0 {
		t.Fatal("unstarted transport claimed fence/drain")
	}
	if err = b.StartIO(); !errors.Is(err, ErrTransport) {
		t.Fatal("IO started before actual backend.Start")
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	for _, f := range b.owned {
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("owned descriptor leaked after start failure")
		}
	}
	if _, err := sink.WriteString("caller still owns sink"); err != nil {
		t.Fatal(err)
	}
}

func TestTransportOwnedSubprocess(t *testing.T) {
	role := os.Getenv("PAYLOAD_TRANSPORT_TEST_ROLE")
	if role == "" {
		return
	}
	if role == "hold" {
		for {
			time.Sleep(time.Second)
		}
	}
	if role == "close-input" {
		_ = os.Stdin.Close()
		os.Exit(0)
	}
	var header [4]byte
	if _, err := io.ReadFull(os.Stdin, header[:]); err != nil {
		os.Exit(71)
	}
	n := binary.BigEndian.Uint32(header[:])
	if n > payloadexec.MaxRequest {
		os.Exit(72)
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(os.Stdin, raw); err != nil {
		os.Exit(73)
	}
	request, err := payloadexec.ParseRequest(raw)
	if err != nil {
		os.Exit(74)
	}
	remaining, err := io.ReadAll(os.Stdin)
	if err != nil || request.Kind == "payload" && len(remaining) != 0 {
		os.Exit(75)
	}
	switch role {
	case "echo-frame":
		_, _ = os.Stdout.Write(header[:])
		_, _ = os.Stdout.Write(raw)
		_, _ = os.Stdout.Write(remaining)
	case "output":
		_, _ = os.Stdout.Write([]byte("stdout\x00bytes\n"))
		_, _ = os.Stderr.Write([]byte("stderr\n"))
	case "wait":
		for {
			time.Sleep(time.Second)
		}
	default:
		os.Exit(76)
	}
	os.Exit(0)
}

func TestFenceFailureCannotAuthorizeDrainEvenAfterLeaderAndEOF(t *testing.T) {
	cmd := exec.Command("payload")
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	startBridge(t, b, "output")
	if err = b.WaitLeader(); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("fixture fence authority unavailable")
	ctx, cancel := bounded(t)
	defer cancel()
	called := 0
	err = b.Drain(ctx, fenceFunc(func(*execution.Execution) error { called++; return denied }))
	if !errors.Is(err, denied) || called != 1 {
		t.Fatal("leader exit or pump EOF replaced mandatory fence", err)
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		t.Fatal("uncertain fence silently claimed successful drain")
	}
}

func TestClosingTransportCannotPreventReapingStartedLeader(t *testing.T) {
	cmd := exec.Command("payload")
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatal(err)
	}
	startBridge(t, b, "wait")
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Kill()
	reaped := make(chan error, 1)
	go func() { reaped <- b.WaitLeader() }()
	select {
	case err := <-reaped:
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("closed transport blocked actual leader reap: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leader reap hung after IO cleanup")
	}
}

func TestLauncherEnvironmentUsesOnlyTrustedPathAndStrictAmbientAllowlist(t *testing.T) {
	cmd := exec.Command("payload")
	cmd.Env = []string{"PATH=/malicious", "HOME=/daemon/private", "TMPDIR=/daemon/private", "GIT_CONFIG_COUNT=1", "LD_PRELOAD=private", "BENCH_LEASE_TOKEN=private", "npm_config_cache=private", "SUDO_UID=0", "LANG=C", "CI=true"}
	b, err := Prepare(cmd, targetFixture("payload"), scopeFixture(), GitInput{})
	if err != nil {
		t.Fatalf("valid launch rejected before transport: %v", err)
	}
	defer b.Close()
	if !reflect.DeepEqual(cmd.Env, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "CI=true"}) {
		t.Fatalf("launcher adopted ambient authority: %q", cmd.Env)
	}
	for _, ambient := range [][]string{{"LANG=C", "LANG=C"}, {"CI=" + strings.Repeat("x", 257)}, {"LC_ALL=\x00"}} {
		candidate := exec.Command("original-payload", "keep")
		candidate.Env = ambient
		originalPath := candidate.Path
		originalArgs := append([]string(nil), candidate.Args...)
		got, err := Prepare(candidate, targetFixture("payload"), scopeFixture(), GitInput{})
		if got != nil || !errors.Is(err, payloadexec.ErrPolicy) || candidate.Path != originalPath || !reflect.DeepEqual(candidate.Args, originalArgs) || candidate.Stdin != nil || candidate.Stdout != nil {
			t.Fatal("invalid ambient input mutated command or allocated a usable transport", err)
		}
	}
	if sameWriter(nil, nil) || sameWriter(nil, io.Discard) || sameWriter(io.Discard, nil) {
		t.Fatal("nil sink comparison must remain safe and unequal")
	}
}
