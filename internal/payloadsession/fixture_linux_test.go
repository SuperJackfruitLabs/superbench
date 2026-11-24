//go:build linux && serviceidentityfixture

package payloadsession_test

// Opt-in root fixture for bench-payload's session mode, run by
// scripts/check-identity-ci.py in private mount and network namespaces under a
// bounded cgroup. The real payloadexec.Session runs in a root child born in a
// remote scope, fed by a daemon-owned control socket and daemon-owned pipes or
// PTY, and execs the real fixed shell. This test binary stands in for sudo's
// caller and for bench-sftp; it is not a sudo, SSH or service-lifecycle claim.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

// TestMain doubles as the fixed bench-sftp: the session execs this binary
// there with argv ["bench-sftp"] and it reports where it landed and as whom.
// It is also bench-payload's main for the session's PID 1 stage, which the
// root stage starts by re-executing this binary as ["bench-payload"].
func TestMain(m *testing.M) {
	if len(os.Args) == 1 && os.Args[0] == "bench-payload" {
		err := errors.New("not the session's PID 1 stage")
		if payloadexec.SessionChild() {
			err = payloadexec.SessionInit()
		}
		fmt.Fprintf(os.Stderr, "managed payload refused: %s\n", payloadexec.RefusalCode(err))
		os.Exit(1)
	}
	if len(os.Args) == 1 && os.Args[0] == "bench-sftp" {
		var st syscall.Stat_t
		if err := syscall.Stat(".", &st); err != nil {
			os.Exit(3)
		}
		fmt.Printf("SFTP_CWD %d:%d UID %d ARGS %d\n", st.Dev, st.Ino, os.Getuid(), len(os.Args))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestRemoteSessionHelperChild is bench-payload's main for session mode.
func TestRemoteSessionHelperChild(t *testing.T) {
	if os.Getenv("SUPERBENCH_SESSION_HELPER") != "1" {
		return
	}
	if !payloadexec.SessionStdin() {
		fmt.Fprintln(os.Stderr, "managed payload refused: stdin")
		os.Exit(1)
	}
	err := payloadexec.Session()
	fmt.Fprintf(os.Stderr, "managed payload refused: %s\n", payloadexec.RefusalCode(err))
	os.Exit(1)
}

func namespaceGuard(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUPERBENCH_IDENTITY_FIXTURE") != "1" {
		t.Fatal("explicit root identity fixture required")
	}
	pid, err := os.Readlink("/proc/self/ns/pid")
	initial, initialErr := os.Readlink("/proc/1/ns/pid")
	if err != nil || initialErr != nil || pid != initial {
		t.Fatal("fixture requires original PID namespace")
	}
	for _, kind := range []string{"mnt", "net"} {
		expected := os.Getenv("SUPERBENCH_IDENTITY_ORIGINAL_" + strings.ToUpper(kind))
		original, err := os.Readlink("/proc/1/ns/" + kind)
		current, currentErr := os.Readlink("/proc/self/ns/" + kind)
		if err != nil || currentErr != nil || expected != original || !strings.HasPrefix(original, kind+":[") || current == original {
			t.Fatalf("private %s namespace not proven", kind)
		}
	}
}

type fixture struct {
	t        *testing.T
	p        payloadpolicy.Policy
	digest   string
	layoutID string
	scope    execution.Execution
	bench    syscall.Stat_t
	created  []string
}

var fixturePolicy = payloadpolicy.Policy{DaemonUID: 61001, DaemonGID: 61003, PayloadUID: 61002, PayloadGID: 61004}

func TestInstalledRemoteSessionFixture(t *testing.T) {
	namespaceGuard(t) // Before any mount, file or child.
	cgroupRoot := os.Getenv("SUPERBENCH_EXECUTION_CGROUP_ROOT")
	if !strings.HasPrefix(cgroupRoot, "/sys/fs/cgroup/superbench-identity-ci-") {
		t.Fatal("the identity CI cgroup is required")
	}
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	machine, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	// The roots may not live under shared scratch (/tmp, /var/tmp, /dev/shm),
	// which every session covers with its own tmpfs: use a private tmpfs.
	must(t, syscall.Mount("tmpfs", "/mnt", "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0755,size=64m"))
	defer func() {
		if err := syscall.Unmount("/mnt", syscall.MNT_DETACH); err != nil {
			t.Error(err)
		}
	}()
	base, err := os.MkdirTemp("/mnt", "superbench-session-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, p: fixturePolicy}
	f.p.DataRoot, f.p.StateRoot, f.p.CgroupRoot = filepath.Join(base, "data"), filepath.Join(base, "state"), cgroupRoot
	must(t, os.Chmod(base, 0755))
	etc := filepath.Join(base, "etc")
	f.mkdir(etc, 0, 0, 0755)
	f.mkdir(filepath.Join(etc, "superbench"), 0, 0, 0755)
	f.write(filepath.Join(etc, "machine-id"), machine, 0, 0, 0644)
	must(t, syscall.Mount(etc, "/etc", "", syscall.MS_BIND, ""))
	must(t, syscall.Mount("tmpfs", "/usr/local/libexec", "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0755,size=256m"))
	scopeID := randomID(t)
	scopePath := filepath.Join(cgroupRoot, scopeID)
	defer func() {
		_ = os.Remove(scopePath) // Empty by now: every child was waited for.
		if err := syscall.Unmount("/usr/local/libexec", 0); err != nil {
			t.Error(err)
		}
		if err := syscall.Unmount("/etc", 0); err != nil {
			t.Error(err)
			return
		}
		if t.Failed() {
			t.Logf("failed session fixture was at %s (private tmpfs)", base)
			return
		}
		// Exact, nonrecursive cleanup of what this fixture created.
		for i := len(f.created) - 1; i >= 0; i-- {
			if err := os.Remove(f.created[i]); err != nil {
				t.Error(err)
			}
		}
		if err := os.Remove(base); err != nil {
			t.Error(err)
		}
	}()
	self, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile("/usr/local/libexec/bench-sftp", self, 0755))
	f.install(etc)
	digest := sha256.Sum256([]byte("superbench-host:" + strings.TrimSpace(string(machine))))
	f.scope = execution.Execution{ID: scopeID, Root: cgroupRoot, HostID: hex.EncodeToString(digest[:]), BootID: strings.TrimSpace(string(boot)), Phase: "running", Kind: execution.KindRemote}
	// Join, never create: a scope that does not exist refuses the start.
	if err := (execution.Host{}).Start(exec.Command("/usr/bin/true"), &f.scope); err == nil {
		t.Fatal("a session helper started in a scope that does not exist")
	}
	if _, err := os.Stat(scopePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("starting created the missing scope")
	}
	must(t, os.Mkdir(scopePath, 0755))

	f.proveNamespace()
	f.proveTerminal()
	f.proveSFTP()
	f.proveRefusals()
	f.proveLifecycle()
}

// proveLifecycle: SIGTERM to the root stage ends the session with 128+15 even
// when its shell ignores the signal (PID 1 tears the namespace down after the
// grace period), and SIGKILL to the root stage kills PID 1 and everything under
// it (parent-death signal). The scope empties either way.
func (f *fixture) proveLifecycle() {
	t := f.t
	for _, c := range []struct {
		signal  syscall.Signal
		command string
	}{{syscall.SIGTERM, "trap '' TERM; echo READY; while :; do sleep 1; done"}, {syscall.SIGKILL, "echo READY; sleep 60"}} {
		in, out, errOut := f.pipe(), f.pipe(), f.pipe()
		s := f.launch(payloadwire.SessionRequest{Mode: "exec", Command: c.command}, []*os.File{in[0], out[1], errOut[1]})
		closeFiles(in[0], out[1], errOut[1])
		line, err := bufio.NewReader(out[0]).ReadString('\n')
		if err != nil || line != "READY\n" || s.controlOutput() != payloadwire.ExecReached {
			t.Fatalf("%v session did not start: %q %v %s", c.signal, line, err, s.helperErr())
		}
		start := time.Now()
		must(t, s.cmd.Process.Signal(c.signal))
		err = s.wait()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || c.signal == syscall.SIGTERM && exit.ExitCode() != 128+int(syscall.SIGTERM) {
			t.Fatalf("%v: root stage ended with %v", c.signal, err)
		}
		procs := filepath.Join(f.scope.Root, f.scope.ID, "cgroup.procs")
		for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			raw, err := os.ReadFile(procs)
			must(t, err)
			if len(strings.TrimSpace(string(raw))) == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%v: the session outlived its root stage: %s", c.signal, raw)
			}
		}
		closeFiles(in[1])
		t.Logf("%v to the root stage: %v, scope empty after %v", c.signal, err, time.Since(start).Round(10*time.Millisecond))
	}
	t.Log("SESSION_ENDS_WITH_ITS_ROOT_STAGE")
}

// proveNamespace runs an exec session over pipes and observes it from inside
// (its shell) and outside (root reading /proc while it waits on stdin).
func (f *fixture) proveNamespace() {
	t, p := f.t, f.p
	data, state := p.DataRoot, p.StateRoot
	// Another bench's live payload-uid process in the host PID namespace, and
	// the cross-bench read it allows from outside a session (positive control).
	other := exec.Command("/usr/bin/setpriv", "--reuid="+strconv.Itoa(int(p.PayloadUID)), "--regid="+strconv.Itoa(int(p.PayloadGID)), "--clear-groups", "/usr/bin/sleep", "120")
	other.Dir = filepath.Join(data, "benches", "other")
	must(t, other.Start())
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	otherPID := strconv.Itoa(other.Process.Pid)
	var leaked []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		leaked, _ = exec.Command("/usr/bin/setpriv", "--reuid="+strconv.Itoa(int(p.PayloadUID)), "--regid="+strconv.Itoa(int(p.PayloadGID)), "--clear-groups", "/usr/bin/cat", "/proc/"+otherPID+"/cwd/secret").Output()
		if string(leaked) == "other bench\n" {
			break
		}
	}
	if string(leaked) != "other bench\n" {
		t.Fatalf("positive leaked: the payload user outside a session could not read the other bench: %q", leaked)
	}
	t.Logf("outside a session, the payload user reads /proc/%s/cwd/secret: %q", otherPID, leaked)
	hostPIDNS, err := os.Readlink("/proc/self/ns/pid")
	must(t, err)
	shared := f.sharedOutside()
	script := strings.Join([]string{
		`for d in /tmp /var/tmp /dev/shm /dev/mqueue; do echo "SHARED $d=$(ls -A $d | tr '\n' ' ')"; done`,
		`echo "IPC_OBJECTS=$(ipcs -q -m -s | grep -c '^0x')"`,
		`echo own > /tmp/own && echo "OWN_TMP=$(cat /tmp/own)"`,
		`echo own > /var/tmp/own && echo "OWN_VAR_TMP=$(cat /var/tmp/own)"`,
		`echo own > /dev/shm/own && echo "OWN_SHM=$(cat /dev/shm/own)"`,
		`echo "OWN_QUEUE=$(ipcmk -Q 2>&1 | grep -c 'Message queue id')"`,
		`unshare -U true 2>&1 | sed 's/^/USERNS /'; echo "USERNS_EXIT=${PIPESTATUS[0]}"`,
		`unshare -Ur true 2>&1 | sed 's/^/USERNS_ROOT /'`,
		`echo "PIDS=$(ls /proc | grep -E '^[0-9]+$' | sort -n | tr '\n' ' ')"`,
		`echo "PIDNS=$(readlink /proc/self/ns/pid)"`,
		`test -e /proc/` + otherPID + ` && echo OTHER_PROC_VISIBLE`,
		`cat /proc/` + otherPID + `/cwd/secret 2>&1 | sed 's/^/OTHER_READ /'`,
		`ls /proc/` + otherPID + `/root/ 2>&1 | head -1 | sed 's/^/OTHER_ROOT /'`,
		`echo "UID=$(id -u)"`,
		`grep '^Seccomp:' /proc/self/status | tr '\t' ' '`,
		`echo "CGROUP=$(cat /proc/self/cgroup)"`,
		`echo "CWD=$(stat -c '%d:%i' .)"`,
		`echo "DATA=$(ls -A ` + data + ` | tr '\n' ' ')"`,
		`echo "BENCHES=$(ls -A ` + data + `/benches | tr '\n' ' ')"`,
		`echo "STATE=$(ls -A ` + state + ` | tr '\n' ' ')"`,
		`test -e ` + data + `/benches/other/secret && echo OTHER_VISIBLE`,
		`echo "HELLO=$(cat hello)"`,
		`touch ` + data + `/x 2>&1 | sed 's/^/TOUCH_DATA /'`,
		`touch ` + state + `/x 2>&1 | sed 's/^/TOUCH_STATE /'`,
		`touch written && echo BENCH_WRITABLE`,
		`echo "REVEAL=$(unshare -Urm sh -c 'umount -l ` + data + `/benches/example; umount -l ` + data + `; ls -A ` + data + `/benches' 2>&1 | tr '\n' ' ')"`,
		`echo "REVEAL_ROOT=$(unshare -Urm sh -c 'umount -l ` + data + ` && echo UNMOUNTED' 2>&1 | tr '\n' ' ')"`,
		`umount -l ` + data + ` 2>/dev/null; echo "REVEAL_DIRECT=$(ls -A ` + data + `/benches | tr '\n' ' ')"`,
		`echo READY`,
		`read -r line`,
		`echo DONE`,
	}, "\n")
	in, out, errOut := f.pipe(), f.pipe(), f.pipe()
	s := f.launch(payloadwire.SessionRequest{Mode: "exec", Command: script}, []*os.File{in[0], out[1], errOut[1]})
	closeFiles(in[0], out[1], errOut[1])
	stderr := make(chan string, 1)
	go func() { raw, _ := io.ReadAll(errOut[0]); stderr <- string(raw) }()
	lines := bufio.NewReader(out[0])
	var seen []string
	for {
		line, err := lines.ReadString('\n')
		if err != nil {
			t.Fatalf("session ended before READY: %v %q\nhelper: %s", err, seen, s.helperErr())
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "READY" {
			break
		}
		seen = append(seen, line)
	}
	control := s.controlOutput()
	if control != payloadwire.ExecReached {
		t.Fatalf("control socket: %q", control)
	}
	// Outside view while the shell waits in its read builtin. The shell is a
	// grandchild of the helper: find it in the scope.
	procs, err := os.ReadFile(filepath.Join(f.scope.Root, f.scope.ID, "cgroup.procs"))
	must(t, err)
	pid, scoped := "", strings.Fields(string(procs))
	for _, candidate := range scoped {
		if comm, _ := os.ReadFile("/proc/" + candidate + "/comm"); string(comm) == "bash\n" {
			pid = candidate
		}
	}
	if len(scoped) != 3 || pid == "" {
		t.Fatalf("scope holds %v, want the root stage, PID 1 and the shell", scoped)
	}
	f.rootStageHoldsNothing(s.cmd.Process.Pid)
	fds, err := os.ReadDir("/proc/" + pid + "/fd")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, fd := range fds {
		names = append(names, fd.Name())
	}
	environ, err := os.ReadFile("/proc/" + pid + "/environ")
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Split(strings.TrimSuffix(string(environ), "\x00"), "\x00")
	slices.Sort(env)
	closeFiles(in[1])
	rest, _ := io.ReadAll(lines)
	if !strings.Contains(string(rest), "DONE") {
		t.Fatalf("session did not finish: %q", rest)
	}
	if err := s.wait(); err != nil {
		t.Fatalf("session exit: %v\nstderr: %s", err, <-stderr)
	}
	errText := <-stderr
	observed := strings.Join(seen, "\n")
	t.Logf("session observed:\n%s\nstderr: %s\nfds: %v\nenv: %v", observed, errText, names, env)
	has := func(line string) bool { return slices.Contains(seen, line) }
	benchID := fmt.Sprintf("CWD=%d:%d", f.bench.Dev, f.bench.Ino)
	if !has("DATA=benches ") || !has("BENCHES=example ") || !has("STATE=") || has("OTHER_VISIBLE") || !has("HELLO=visible") || !has(benchID) || !has("BENCH_WRITABLE") {
		t.Fatalf("the namespace showed more than its bench:\n%s", observed)
	}
	t.Log("SESSION_NAMESPACE_SHOWS_ONLY_ITS_BENCH")
	if !strings.Contains(observed, "TOUCH_DATA touch: cannot touch '"+data+"/x': Read-only file system") || !strings.Contains(observed, "TOUCH_STATE touch: cannot touch '"+state+"/x': Read-only file system") {
		t.Fatalf("the tmpfs roots are writable:\n%s", observed)
	}
	t.Log("SESSION_TMPFS_READ_ONLY")
	for _, line := range seen {
		if strings.HasPrefix(line, "REVEAL") && (strings.Contains(line, "other") || strings.Contains(line, "UNMOUNTED")) {
			t.Fatalf("the session undid its mounts: %s", line)
		}
	}
	if !has("REVEAL_DIRECT=example ") {
		t.Fatalf("a direct unmount changed the view:\n%s", observed)
	}
	t.Log("SESSION_CANNOT_UNMOUNT_TO_REVEAL")
	if !has("UID="+strconv.Itoa(int(f.p.PayloadUID))) || !has("Seccomp: 2") {
		t.Fatalf("not the confined payload user:\n%s", observed)
	}
	t.Log("SESSION_PAYLOAD_UID_AND_SECCOMP")
	if !has("CGROUP=0::" + strings.TrimPrefix(f.scope.Root, "/sys/fs/cgroup") + "/" + f.scope.ID) {
		t.Fatalf("session left its remote scope:\n%s", observed)
	}
	t.Log("SESSION_STAYS_IN_REMOTE_SCOPE")
	var pids []string
	for _, line := range seen {
		if rest, ok := strings.CutPrefix(line, "PIDS="); ok {
			pids = strings.Fields(rest)
		}
	}
	for _, pid := range pids {
		if n, err := strconv.Atoi(pid); err != nil || n > 64 {
			t.Fatalf("the session's /proc lists %v", pids)
		}
	}
	// PID 1 runs as the payload user but is not dumpable after its drop, so
	// hidepid=2 hides it too: the shell sees only its own processes.
	if len(pids) == 0 || slices.Contains(pids, "1") || has("PIDNS="+hostPIDNS) || has("OTHER_PROC_VISIBLE") || !has("OTHER_READ cat: /proc/"+otherPID+"/cwd/secret: No such file or directory") || !has("OTHER_ROOT ls: cannot access '/proc/"+otherPID+"/root/': No such file or directory") {
		t.Fatalf("the session's /proc shows more than the session:\n%s", observed)
	}
	t.Log("SESSION_PROC_SHOWS_ONLY_ITS_SESSION")
	for _, d := range []string{"/tmp", "/var/tmp", "/dev/shm", "/dev/mqueue"} {
		if !has("SHARED " + d + "=") {
			t.Fatalf("the session's %s is not its own:\n%s", d, observed)
		}
	}
	if !has("IPC_OBJECTS=0") || !has("OWN_TMP=own") || !has("OWN_VAR_TMP=own") || !has("OWN_SHM=own") || !has("OWN_QUEUE=1") {
		t.Fatalf("shared tmp or IPC is not private, or the session's own does not work:\n%s", observed)
	}
	shared.stillThere()
	t.Log("SESSION_SHARED_TMP_AND_IPC_ARE_PRIVATE")
	if !has("USERNS unshare: unshare failed: Operation not permitted") || has("USERNS_EXIT=0") || !has("USERNS_ROOT unshare: unshare failed: Operation not permitted") {
		t.Fatalf("the session can create a user namespace:\n%s", observed)
	}
	t.Log("SESSION_NO_USER_NAMESPACE")
	// No descriptor beyond the session's three, and only the fixed environment.
	if !slices.Equal(names, []string{"0", "1", "2"}) {
		t.Fatalf("the shell inherited descriptors %v", names)
	}
	home := filepath.Join(f.p.DataRoot, "benches", "example", ".superbench-runtime", ".root")
	want := []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + home + "/home", "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin", "TMPDIR=" + home + "/tmp"}
	if !slices.Equal(env, want) {
		t.Fatalf("session environment %v, want %v", env, want)
	}
}

// outsideObjects are same-uid objects another bench's process left in the
// host's shared scratch and IPC namespace.
type outsideObjects struct {
	t     *testing.T
	files []string
	ipc   [][2]string // ipcrm flag, id
}

// sharedOutside writes payload-owned files in /tmp, /var/tmp and /dev/shm and
// creates a payload-owned SysV queue and segment in the host IPC namespace,
// all as another bench's run would, and removes them when the test ends.
func (f *fixture) sharedOutside() *outsideObjects {
	t, p := f.t, f.p
	o := &outsideObjects{t: t}
	name := "superbench-session-outside-" + randomID(t)
	for _, d := range []string{"/tmp", "/var/tmp", "/dev/shm"} {
		path := filepath.Join(d, name)
		must(t, os.WriteFile(path, []byte("other bench\n"), 0600))
		o.files = append(o.files, path)
		must(t, os.Chown(path, int(p.PayloadUID), int(p.PayloadGID)))
	}
	asPayload := []string{"--reuid=" + strconv.Itoa(int(p.PayloadUID)), "--regid=" + strconv.Itoa(int(p.PayloadGID)), "--clear-groups", "/usr/bin/ipcmk"}
	for flag, kind := range map[string]string{"-q": "-Q", "-m": "-M"} {
		args := append(append([]string{}, asPayload...), kind)
		if kind == "-M" {
			args = append(args, "4096")
		}
		out, err := exec.Command("/usr/bin/setpriv", args...).CombinedOutput()
		fields := strings.Fields(string(out))
		if err != nil || len(fields) == 0 {
			t.Fatalf("ipcmk %s: %v %s", kind, err, out)
		}
		o.ipc = append(o.ipc, [2]string{flag, fields[len(fields)-1]})
	}
	t.Cleanup(func() {
		for _, path := range o.files {
			_ = os.Remove(path)
		}
		for _, object := range o.ipc {
			_ = exec.Command("/usr/bin/ipcrm", object[0], object[1]).Run()
		}
	})
	t.Logf("outside the session: payload-owned %v and SysV objects %v", o.files, o.ipc)
	return o
}

// stillThere proves the outside objects existed all along (so their absence
// inside the session is the namespace's doing).
func (o *outsideObjects) stillThere() {
	for _, path := range o.files {
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "other bench\n" {
			o.t.Fatalf("outside object %s vanished: %v", path, err)
		}
	}
	for _, object := range o.ipc {
		if out, err := exec.Command("/usr/bin/ipcs", object[0], "-i", object[1]).CombinedOutput(); err != nil {
			o.t.Fatalf("outside SysV object %v vanished: %v %s", object, err, out)
		}
	}
}

// rootStageHoldsNothing: after the fork the root stage keeps only /dev/null on
// fd 0, the daemon's two refusal pipes, the handoff socket and runtime
// anonymous inodes: no bench, scope, state, policy or session descriptor.
func (f *fixture) rootStageHoldsNothing(pid int) {
	t := f.t
	dir := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(dir)
	must(t, err)
	var held []string
	sockets := 0
	for _, entry := range entries {
		target, err := os.Readlink(dir + "/" + entry.Name())
		must(t, err)
		held = append(held, entry.Name()+"="+target)
		switch fd := entry.Name(); {
		case fd == "0" && target == "/dev/null":
		case (fd == "1" || fd == "2") && strings.HasPrefix(target, "pipe:"):
		case strings.HasPrefix(target, "anon_inode:"):
		case strings.HasPrefix(target, "socket:") && sockets == 0:
			sockets++
		default:
			t.Fatalf("the root stage keeps %s=%s after the fork: %v", fd, target, held)
		}
	}
	t.Logf("root stage descriptors after the fork: %v", held)
}

// proveTerminal runs an exec session on a PTY: the slave becomes the payload
// user's, mode 0600, and the session's controlling terminal.
func (f *fixture) proveTerminal() {
	t := f.t
	master, slave := f.pty()
	defer master.Close()
	s := f.launch(payloadwire.SessionRequest{Mode: "exec", Command: `t=$(tty); echo "TTY=$t"; echo "OWNER=$(stat -c '%u %a' "$t")"; echo "TERM=$TERM"; exec 3</dev/tty && echo CTTY_OK`, Term: "xterm-256color"}, []*os.File{slave})
	closeFiles(slave)
	if control := s.controlOutput(); control != payloadwire.ExecReached {
		t.Fatalf("control socket: %q\nhelper: %s", control, s.helperErr())
	}
	var output bytes.Buffer
	_, _ = io.Copy(&output, master) // Ends with EIO once the session's side closes.
	if err := s.wait(); err != nil {
		t.Fatalf("PTY session exit: %v\n%s", err, output.String())
	}
	text := strings.ReplaceAll(output.String(), "\r\n", "\n")
	t.Logf("PTY session:\n%s", text)
	if !regexp.MustCompile(`(?m)^TTY=/dev/pts/[0-9]+$`).MatchString(text) || !strings.Contains(text, "OWNER="+strconv.Itoa(int(f.p.PayloadUID))+" 600\n") || !strings.Contains(text, "TERM=xterm-256color\n") || !strings.Contains(text, "CTTY_OK\n") {
		t.Fatalf("PTY is not the payload user's controlling terminal:\n%s", text)
	}
	// The shell mode on a PTY: a login bash reads its commands from the terminal.
	master, slave = f.pty()
	defer master.Close()
	s = f.launch(payloadwire.SessionRequest{Mode: "shell", Term: "xterm"}, []*os.File{slave})
	closeFiles(slave)
	if control := s.controlOutput(); control != payloadwire.ExecReached {
		t.Fatalf("shell control socket: %q\nhelper: %s", control, s.helperErr())
	}
	output.Reset()
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, 4096)
			n, err := master.Read(buf)
			if n > 0 {
				chunks <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	typed := false
	for chunk := range chunks {
		output.Write(chunk)
		if !typed && strings.Contains(output.String(), "$ ") {
			// A login shell saves history on exit; this one must leave no file.
			_, _ = master.WriteString("unset HISTFILE; echo SHELL_$((6*7)) $(id -u) $(stat -c '%d:%i' .)\nexit\n")
			typed = true
		}
	}
	if err := s.wait(); err != nil {
		t.Fatalf("shell exit: %v\n%s", err, output.String())
	}
	if want := fmt.Sprintf("SHELL_42 %d %d:%d", f.p.PayloadUID, f.bench.Dev, f.bench.Ino); !strings.Contains(output.String(), want) {
		t.Fatalf("shell session: %q", output.String())
	}
	t.Log("SESSION_PTY_CONTROLLING_TERMINAL")
}

// proveSFTP: the fixed bench-sftp starts with argv ["bench-sftp"], as the
// payload user, in the workspace root.
func (f *fixture) proveSFTP() {
	t := f.t
	in, out, errOut := f.pipe(), f.pipe(), f.pipe()
	s := f.launch(payloadwire.SessionRequest{Mode: "sftp"}, []*os.File{in[0], out[1], errOut[1]})
	closeFiles(in[0], out[1], errOut[1], in[1])
	raw, _ := io.ReadAll(out[0])
	if control := s.controlOutput(); control != payloadwire.ExecReached {
		t.Fatalf("sftp control socket: %q\nhelper: %s", control, s.helperErr())
	}
	if err := s.wait(); err != nil {
		t.Fatalf("sftp exit: %v", err)
	}
	if want := fmt.Sprintf("SFTP_CWD %d:%d UID %d ARGS 1\n", f.bench.Dev, f.bench.Ino, f.p.PayloadUID); string(raw) != want {
		t.Fatalf("bench-sftp started elsewhere: %q, want %q", raw, want)
	}
	t.Logf("bench-sftp: %s", strings.TrimSpace(string(raw)))
}

// proveRefusals: a wrong descriptor count or kind refuses with the static code
// on stderr, and ExecReached never reaches the control socket.
func (f *fixture) proveRefusals() {
	t := f.t
	a, b := f.pipe(), f.pipe()
	master, slave := f.pty()
	defer master.Close()
	defer slave.Close()
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	sock, peer := os.NewFile(uintptr(pair[0]), "socket"), os.NewFile(uintptr(pair[1]), "peer")
	defer closeFiles(sock, peer)
	for name, c := range map[string]struct {
		term string
		fds  []*os.File
	}{
		"two descriptors":  {"", []*os.File{a[0], a[1]}},
		"four descriptors": {"", []*os.File{a[0], a[1], b[1], b[1]}},
		"reversed ends":    {"", []*os.File{a[1], a[0], b[1]}},
		"socket stdin":     {"", []*os.File{sock, a[1], b[1]}},
		"pipe as PTY":      {"xterm", []*os.File{a[0]}},
		"PTY as pipes":     {"", []*os.File{slave, slave, slave}},
		"three for PTY":    {"xterm", []*os.File{slave, slave, slave}},
	} {
		s := f.launch(payloadwire.SessionRequest{Mode: "exec", Command: "echo EXECUTED", Term: c.term}, c.fds)
		control := s.controlOutput()
		err := s.wait()
		if control != "" || err == nil || s.helperErr() != "managed payload refused: descriptors\n" {
			t.Fatalf("%s: control %q, exit %v, stderr %q", name, control, err, s.helperErr())
		}
	}
	t.Log("SESSION_DESCRIPTOR_REFUSALS_NO_EXEC")
}

// launched is one helper child: its control socket and refusal stderr.
type launched struct {
	t       *testing.T
	cmd     *exec.Cmd
	control *os.File
	stderr  chan string
	errText *string
}

func (f *fixture) launch(r payloadwire.SessionRequest, send []*os.File) *launched {
	t := f.t
	r.Version, r.Key, r.LayoutID, r.PolicyDigest = payloadwire.SessionVersion, "example", f.layoutID, f.digest
	r.Scope = payloadwire.Scope{ID: f.scope.ID, HostID: f.scope.HostID, BootID: f.scope.BootID}
	body, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var pair [2]int
	f.asDaemon(func() error {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
		pair = [2]int{fds[0], fds[1]}
		return err
	})
	must(t, syscall.SetNonblock(pair[0], true)) // A pollable control end, for deadlines.
	control, child := os.NewFile(uintptr(pair[0]), "control"), os.NewFile(uintptr(pair[1]), "helper stdin")
	out, errOut := f.pipe(), f.pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "/proc/self/exe", "-test.run=^TestRemoteSessionHelperChild$", "-test.timeout=50s")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "SUPERBENCH_SESSION_HELPER=1", "SUPERBENCH_DAEMON_SECRET=never-in-a-session", "LANG=C.UTF-8", "TERM=daemon-term"}
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = "/", child, out[1], errOut[1]
	cmd.WaitDelay = time.Second
	if err := (execution.Host{}).Start(cmd, &f.scope); err != nil {
		t.Fatal(err)
	}
	closeFiles(child, out[1], errOut[1])
	go func() { _, _ = io.Copy(io.Discard, out[0]) }()
	s := &launched{t: t, cmd: cmd, control: control, stderr: make(chan string, 1)}
	go func() { raw, _ := io.ReadAll(io.LimitReader(errOut[0], 4<<10)); s.stderr <- string(raw) }()
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	if _, err := control.Write(append(frame, body...)); err != nil {
		t.Fatal(err)
	}
	var fds []int
	for _, file := range send {
		fds = append(fds, int(file.Fd()))
	}
	raw, err := control.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var sendErr error
	must(t, raw.Write(func(fd uintptr) bool {
		sendErr = syscall.Sendmsg(int(fd), []byte{'F'}, syscall.UnixRights(fds...), nil, 0)
		return sendErr != syscall.EAGAIN
	}))
	must(t, sendErr)
	must(t, raw.Control(func(fd uintptr) { sendErr = syscall.Shutdown(int(fd), syscall.SHUT_WR) }))
	must(t, sendErr)
	return s
}

// controlOutput is everything the helper wrote on the control socket before
// its exec closed it (close-on-exec) or it exited.
func (s *launched) controlOutput() string {
	must(s.t, s.control.SetReadDeadline(time.Now().Add(30*time.Second)))
	raw, err := io.ReadAll(s.control)
	if err != nil {
		s.t.Fatal(err)
	}
	_ = s.control.Close()
	return string(raw)
}

func (s *launched) wait() error { return s.cmd.Wait() }

func (s *launched) helperErr() string {
	if s.errText == nil {
		text := <-s.stderr
		s.errText = &text
	}
	return *s.errText
}

// asDaemon runs fn with this thread's effective ids switched to the daemon's,
// so that sockets it creates carry the daemon's peer credentials. fn may use
// only raw system calls: no Go runtime thread is created meanwhile.
func (f *fixture) asDaemon(fn func() error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	keep := ^uintptr(0)
	if _, _, e := syscall.RawSyscall(syscall.SYS_SETRESGID, keep, uintptr(f.p.DaemonGID), keep); e != 0 {
		f.t.Fatal(e)
	}
	if _, _, e := syscall.RawSyscall(syscall.SYS_SETRESUID, keep, uintptr(f.p.DaemonUID), keep); e != 0 {
		f.t.Fatal(e)
	}
	err := fn()
	_, _, uidErr := syscall.RawSyscall(syscall.SYS_SETRESUID, keep, 0, keep)
	_, _, gidErr := syscall.RawSyscall(syscall.SYS_SETRESGID, keep, 0, keep)
	if uidErr != 0 || gidErr != 0 {
		fmt.Fprintln(os.Stderr, "fixture thread could not restore root")
		os.Exit(70)
	}
	must(f.t, err)
}

// pipe is a daemon-owned pipe: [read, write].
func (f *fixture) pipe() [2]*os.File {
	r, w, err := os.Pipe()
	if err != nil {
		f.t.Fatal(err)
	}
	must(f.t, w.Chown(int(f.p.DaemonUID), int(f.p.DaemonGID)))
	f.t.Cleanup(func() { closeFiles(r, w) })
	return [2]*os.File{r, w}
}

// pty is a daemon-owned PTY pair, the slave taken from the master.
func (f *fixture) pty() (*os.File, *os.File) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		f.t.Fatal(errno)
	}
	fd, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x5441, uintptr(syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC))
	if errno != 0 {
		f.t.Fatal(errno)
	}
	slave := os.NewFile(fd, "pty")
	must(f.t, slave.Chown(int(f.p.DaemonUID), int(f.p.DaemonGID)))
	return master, slave
}

// install lays out two registered-shape benches and the helper's policy.
func (f *fixture) install(etc string) {
	t, p := f.t, f.p
	daemon, payload := [2]int{int(p.DaemonUID), int(p.DaemonGID)}, [2]int{int(p.PayloadUID), int(p.PayloadGID)}
	f.mkdir(p.DataRoot, daemon[0], daemon[1], 0700)
	f.mkdir(p.StateRoot, daemon[0], daemon[1], 0700)
	benches := filepath.Join(p.DataRoot, "benches")
	f.mkdir(benches, daemon[0], daemon[1], 0700)
	for _, key := range []string{"example", "other"} {
		root := filepath.Join(benches, key)
		f.mkdir(root, payload[0], payload[1], 0700)
		for _, name := range []string{".superbench-git", ".superbench-runtime", ".superbench-runtime/.root", ".superbench-runtime/.root/home", ".superbench-runtime/.root/tmp", "api", ".superbench-git/api", ".superbench-runtime/api", ".superbench-runtime/api/home", ".superbench-runtime/api/tmp"} {
			f.mkdir(filepath.Join(root, name), payload[0], payload[1], 0700)
		}
	}
	f.write(filepath.Join(benches, "other", "secret"), []byte("other bench\n"), payload[0], payload[1], 0600)
	root := filepath.Join(benches, "example")
	f.write(filepath.Join(root, "hello"), []byte("visible\n"), payload[0], payload[1], 0644)
	f.created = append(f.created, filepath.Join(root, "written")) // The session creates it.
	must(t, syscall.Stat(root, &f.bench))
	node := func(name string) payloadexec.Node {
		var st syscall.Stat_t
		must(t, syscall.Stat(filepath.Join(root, name), &st))
		return payloadexec.Node{Device: uint64(st.Dev), Inode: st.Ino}
	}
	registry, err := serviceidentity.ForPolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	f.digest, f.layoutID = registry.PolicySHA256, strings.Repeat("a", 32)
	l := payloadexec.Layout{Version: payloadexec.LayoutVersion, Key: "example", ID: f.layoutID, PolicyDigest: f.digest, Root: node("."), RootHome: node(".superbench-runtime/.root/home"), RootTemp: node(".superbench-runtime/.root/tmp"), Members: []payloadexec.LayoutMember{{Name: "api", Checkout: node("api"), Admin: node(".superbench-git/api"), Home: node(".superbench-runtime/api/home"), Temp: node(".superbench-runtime/api/tmp")}}}
	raw, err := l.Bytes(p)
	if err != nil {
		t.Fatal(err)
	}
	layouts := filepath.Join(p.StateRoot, "payload-layouts")
	f.mkdir(layouts, daemon[0], daemon[1], 0700)
	f.write(filepath.Join(layouts, "example.json"), raw, daemon[0], daemon[1], 0600)
	policy, err := p.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	public, err := registry.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	f.write(payloadpolicy.FixedPolicy, policy, 0, 0, 0600)
	f.write(serviceidentity.FixedRegistry, public, 0, 0, 0644)
	f.created = append(f.created, filepath.Join(etc, "superbench", "payload-policy.json"), filepath.Join(etc, "superbench", "service-identity.json"))
}

func (f *fixture) mkdir(path string, uid, gid int, mode os.FileMode) {
	f.t.Helper()
	must(f.t, os.Mkdir(path, mode))
	f.created = append(f.created, path)
	must(f.t, os.Chown(path, uid, gid))
	must(f.t, os.Chmod(path, mode))
}

func (f *fixture) write(path string, raw []byte, uid, gid int, mode os.FileMode) {
	f.t.Helper()
	must(f.t, os.WriteFile(path, raw, mode))
	if !strings.HasPrefix(path, "/etc/") {
		f.created = append(f.created, path)
	}
	must(f.t, os.Chown(path, uid, gid))
	must(f.t, os.Chmod(path, mode))
}

func randomID(t *testing.T) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
