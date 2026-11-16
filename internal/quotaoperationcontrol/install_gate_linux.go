//go:build linux

package quotaoperationcontrol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storagecustody"
)

var ErrInstall = errors.New("fresh indexed installation gate refused")

// The fixed service must actually be stopped and persistently masked. The mask
// alone cannot establish inactivity, particularly across a mount namespace.
// This assumes root is not concurrently defeating the installation exclusion;
// neither a policy field nor an operator-supplied assertion proves this gate.
func installationGate(ctx context.Context, p hoststorage.Policy) error {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		return ErrInstall
	}
	account, err := user.Lookup("superbench")
	if err != nil {
		return ErrInstall
	}
	group, err := user.LookupGroup("superbench")
	if err != nil || group.Gid != account.Gid || fixedAccountIDs(account.Uid, account.Gid, p) != nil {
		return ErrInstall
	}
	registry, err := serviceidentity.ResolveHelperRegistry()
	if err != nil {
		return err
	}
	public, err := registry.Policy()
	if err != nil {
		return err
	}
	private, err := payloadexec.LoadFixedPolicy()
	if err != nil {
		return err
	}
	if public != private || private.DaemonUID != uint32(p.OwnerUID) || private.DaemonGID != uint32(p.OwnerGID) || private.DataRoot != p.Root {
		return ErrInstall
	}
	mask := "/etc/systemd/system/superbench.service"
	if err = storagecustody.TrustedAncestors(mask); err != nil {
		return err
	}
	before, err := os.Lstat(mask)
	if err != nil {
		return err
	}
	st := before.Sys().(*syscall.Stat_t)
	if before.Mode()&os.ModeSymlink == 0 || st.Uid != 0 {
		return ErrInstall
	}
	target, err := os.Readlink(mask)
	if err != nil || target != "/dev/null" {
		return ErrInstall
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--no-pager", "--property=LoadState,ActiveState,SubState,MainPID,ControlPID,UnitFileState,Job,User,Group", "superbench.service")
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var out gateOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil || out.over {
		return ErrInstall
	}
	if err = stoppedUnit(out.Bytes()); err != nil {
		return err
	}
	if err = noControllers(ctx, private.DaemonUID, private.PayloadUID); err != nil {
		return err
	}
	after, err := os.Lstat(mask)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		return ErrInstall
	}
	if target, err = os.Readlink(mask); err != nil || target != "/dev/null" {
		return ErrInstall
	}
	return registry.Recheck()
}

// Masked unloaded units legitimately omit the service execution context. A
// present nonempty User/Group still must match; missing context grants no future
// launch permission (the installed process binding rechecks real credentials).
func stoppedUnit(raw []byte) error {
	expected := map[string]string{"LoadState": "masked", "ActiveState": "inactive", "SubState": "dead", "MainPID": "0", "ControlPID": "0", "UnitFileState": "masked", "Job": ""}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || seen[key] {
			return ErrInstall
		}
		seen[key] = true
		if key == "User" || key == "Group" {
			if value != "" && value != "superbench" {
				return ErrInstall
			}
			continue
		}
		want, known := expected[key]
		if !known || want != value {
			return ErrInstall
		}
	}
	for key := range expected {
		if !seen[key] {
			return ErrInstall
		}
	}
	return nil
}

func noControllers(ctx context.Context, daemon, payload uint32) error {
	var fs syscall.Statfs_t
	if err := syscall.Statfs("/proc", &fs); err != nil || fs.Type != 0x9fa0 {
		return ErrInstall
	}
	// A private PID namespace cannot prove the original machine's old writers
	// absent. Require the PID1 namespace reported by the procfs view itself.
	// This is a consistency check, not a proof against a root-created nested
	// PID namespace. Direct-root invocation must be on the original booted host.
	self, err := os.Stat("/proc/self/ns/pid")
	if err != nil {
		return err
	}
	one, err := os.Stat("/proc/1/ns/pid")
	if err != nil || !os.SameFile(self, one) {
		return ErrInstall
	}
	raw, err := readMountInfo()
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 || fullProcMount(raw) != nil {
		return ErrInstall
	}
	dir, err := os.Open("/proc")
	if err != nil {
		return err
	}
	defer dir.Close()
	pinned, err := dir.Stat()
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(131073)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 131072 {
		return ErrInstall
	}
	for _, name := range names {
		if err = ctx.Err(); err != nil {
			return err
		}
		pid, e := strconv.Atoi(name)
		if e != nil || pid <= 0 {
			continue
		}
		fd, e := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		var process *os.File
		if e == nil {
			process = os.NewFile(uintptr(fd), name)
		}
		if errors.Is(e, syscall.ENOENT) || errors.Is(e, syscall.ESRCH) {
			continue
		}
		if e != nil {
			return e
		}
		e = inspectProcess(process, pid, daemon, payload)
		process.Close()
		if errors.Is(e, syscall.ENOENT) || errors.Is(e, syscall.ESRCH) {
			continue
		}
		if e != nil {
			return e
		}
	}
	current, err := os.Stat("/proc")
	if err != nil || !os.SameFile(pinned, current) {
		return ErrInstall
	}
	return ctx.Err()
}

// Filtered proc views are unsuitable for a negative writer inventory. This
// does not turn a nested namespace into proof about an outer booted host.
func fullProcMount(raw []byte) error {
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[4] != "/proc" {
			continue
		}
		count++
		dash := -1
		for j, v := range f {
			if v == "-" {
				dash = j
				break
			}
		}
		if f[3] != "/" || dash < 6 || dash+3 >= len(f) || f[dash+1] != "proc" {
			return ErrInstall
		}
		for _, v := range strings.Split(f[5]+","+f[dash+3], ",") {
			if strings.HasPrefix(v, "subset=") || strings.HasPrefix(v, "hidepid=") && v != "hidepid=0" {
				return ErrInstall
			}
		}
	}
	if count != 1 {
		return ErrInstall
	}
	return nil
}

func refuseIdentity(raw []byte, daemon, payload uint32) error {
	seen := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		if seen {
			return ErrInstall
		}
		seen = true
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if len(fields) != 4 {
			return ErrInstall
		}
		for _, s := range fields {
			v, err := strconv.ParseUint(s, 10, 32)
			if err != nil {
				return ErrInstall
			}
			if uint32(v) == daemon || uint32(v) == payload {
				return ErrInstall
			}
		}
	}
	if !seen {
		return ErrInstall
	}
	return nil
}

// The proc-directory FD pins one process; a recycled numeric PID cannot
// redirect the UID read to one process and executable read to another.
func inspectProcess(process *os.File, pid int, daemon, payload uint32) error {
	fd, err := syscall.Openat(int(process.Fd()), "status", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "status")
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	f.Close()
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return ErrInstall
	}
	if err = refuseIdentity(raw, daemon, payload); err != nil {
		return err
	}
	if pid == os.Getpid() {
		return nil
	}
	exe, err := os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(process.Fd()), 10) + "/exe")
	if err != nil {
		return err
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	switch exe {
	case "/usr/local/libexec/bench-quota", "/usr/local/bin/benchd", "/usr/local/libexec/bench-payload", "/usr/sbin/xfs_quota":
		return ErrInstall
	}
	return nil
}

type gateOutput struct {
	bytes.Buffer
	over bool
}

func (b *gateOutput) Write(raw []byte) (int, error) {
	if b.Len()+len(raw) > 65536 {
		b.over = true
		return 0, ErrInstall
	}
	return b.Buffer.Write(raw)
}

func readMountInfo() ([]byte, error) {
	f, err := os.OpenFile("/proc/self/mountinfo", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, ErrInstall
	}
	return raw, nil
}

// This initializer is intentionally restricted to the installed fixed unit and
// account. It cannot initialize a custom service identity by policy substitution.
func fixedAccountIDs(uid, gid string, p hoststorage.Policy) error {
	u, err := strconv.ParseUint(uid, 10, 32)
	if err != nil || u == 0 || strconv.FormatUint(u, 10) != uid || uint64(p.OwnerUID) != u {
		return ErrInstall
	}
	g, err := strconv.ParseUint(gid, 10, 32)
	if err != nil || g == 0 || strconv.FormatUint(g, 10) != gid || uint64(p.OwnerGID) != g {
		return ErrInstall
	}
	return nil
}
