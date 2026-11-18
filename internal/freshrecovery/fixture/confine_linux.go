//go:build linux && freshstoragefixture

package fixture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshrecovery"
)

const (
	projInherit     = 0x200
	fsIocFsgetxattr = 0x801c581f
	fsIocFssetxattr = 0x401c5820
)

type fsattr struct {
	Xflags, Extsize, Nextents, Projid, Cowextsize uint32
	Pad                                           [8]byte
}

func (e *env) attrCall(path string, cmd uintptr, a *fsattr) {
	e.t.Helper()
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer syscall.Close(fd)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), cmd, uintptr(unsafe.Pointer(a))); errno != 0 {
		e.t.Fatalf("fsxattr %s: %v", path, errno)
	}
}

func (e *env) attr(path string) fsattr {
	e.t.Helper()
	var a fsattr
	e.attrCall(path, fsIocFsgetxattr, &a)
	return a
}

// setAttr is root's restore of a project ID or inherit flag.
func (e *env) setAttr(path string, a fsattr) {
	e.t.Helper()
	e.attrCall(path, fsIocFssetxattr, &a)
}

// refusedEscaped requires the reclaim refused before retention with the
// tree's own error and nothing removed.
func (e *env) refusedEscaped(b bench, kept string) {
	t := e.t
	t.Helper()
	_, err := e.reclaim(t.Context(), b)
	if !errors.Is(err, freshrecovery.ErrReclaimEscaped) || strings.Contains(err.Error(), "effects may have occurred") {
		t.Fatalf("a tree outside its project: %v", err)
	}
	if p := e.intentPhase(b.spec.Key); p != "completed" {
		t.Fatalf("an escaped-tree refusal moved the intent to %s", p)
	}
	assertBytes(t, kept, []byte("source-api\n"))
	if row := e.row(b.spec.Key); row != b.allocation {
		t.Fatalf("an escaped-tree refusal changed the row: %+v", row)
	}
}

// confinedPurge requires every payload-account process in a clean-worker
// cgroup, the running purge worker, to report seccomp filter mode and
// no_new_privs, and at least one to exist.
func (e *env) confinedPurge() error {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	found := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		dir := filepath.Join("/proc", entry.Name())
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		fields := statusFields(status)
		if err != nil || strings.Fields(fields["Uid"]) == nil || strings.Fields(fields["Uid"])[0] != strconv.Itoa(payloadUID) {
			continue
		}
		cgroup, err := os.ReadFile(filepath.Join(dir, "cgroup"))
		if err != nil || !strings.Contains(string(cgroup), "/superbench-clean-") {
			continue
		}
		found++
		if fields["Seccomp"] != "2" || fields["NoNewPrivs"] != "1" {
			return fmt.Errorf("purge worker %s is not confined: %s", entry.Name(), status)
		}
	}
	if found == 0 {
		return errors.New("no purge worker found while the purge ran")
	}
	return nil
}
