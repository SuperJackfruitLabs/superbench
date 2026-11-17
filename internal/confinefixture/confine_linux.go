//go:build linux && freshstoragefixture

// Package confinefixture proves, on the privileged XFS fixture, that the
// payload cannot move its files out of the bench's quota project and that the
// root verifier refuses a leaf that left it. Fixture-only: tag freshstoragefixture.
package confinefixture

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaleaf"
)

const (
	childEnv        = "SUPERBENCH_CONFINE_CHILD_POLICY"
	fsIocFsgetxattr = 0x801c581f
	fsIocFssetxattr = 0x401c5820
	fsIocGetflags   = 0x80086601
	fsIocSetflags   = 0x40086602
	ioUringSetup    = 425
)

type fsxattr struct {
	Xflags, Extsize, Nextents, Projid, Cowextsize uint32
	Pad                                           [8]byte
}

func getattr(fd uintptr) (fsxattr, error) {
	var a fsxattr
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, fsIocFsgetxattr, uintptr(unsafe.Pointer(&a))); e != 0 {
		return a, e
	}
	return a, nil
}

func setattr(fd uintptr, a fsxattr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, fsIocFssetxattr, uintptr(unsafe.Pointer(&a))); e != 0 {
		return e
	}
	return nil
}

// marker prints one exact marker alone on its line.
func marker(m string) { fmt.Println(m) }

// The confined child is this test binary re-executed as root. It runs the
// production drop, which installs the filter, then tries every route the
// owner has to change a project ID or the inherit flag.
func init() {
	raw := os.Getenv(childEnv)
	if raw == "" {
		return
	}
	p, err := payloadexec.ParsePolicy([]byte(raw))
	if err == nil {
		err = payloadexec.FixtureDrop(p)
	}
	if err != nil {
		fmt.Println("CHILD_DROP_FAILED", err)
		os.Exit(3)
	}
	file, dir := uintptr(3), uintptr(4)
	expect := func(name string, err error) {
		if errors.Is(err, syscall.EPERM) {
			fmt.Println(name + "_EPERM")
		} else {
			fmt.Println(name+"_UNEXPECTED", err)
		}
	}
	a, err := getattr(file)
	if err != nil {
		fmt.Println("CHILD_GETATTR_FAILED", err)
		os.Exit(3)
	}
	a.Projid = 0
	expect("CHILD_FSSETXATTR_PROJECT", setattr(file, a))
	d, err := getattr(dir)
	if err != nil {
		fmt.Println("CHILD_GETATTR_FAILED", err)
		os.Exit(3)
	}
	d.Xflags &^= quotaleaf.FlagProjInherit
	expect("CHILD_FSSETXATTR_INHERIT", setattr(dir, d))
	var flags uint64
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, file, fsIocGetflags, uintptr(unsafe.Pointer(&flags))); e != 0 {
		fmt.Println("CHILD_GETFLAGS_FAILED", e)
		os.Exit(3)
	}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, file, fsIocSetflags, uintptr(unsafe.Pointer(&flags)))
	expect("CHILD_SETFLAGS", e)
	var params [128]byte
	_, _, e = syscall.Syscall(ioUringSetup, 1, uintptr(unsafe.Pointer(&params[0])), 0)
	expect("CHILD_IO_URING_SETUP", e)
	// The filter survives exec and fork: the real tool fails in a grandchild.
	err = syscall.Exec("/bin/sh", []string{"sh", "-c", "/usr/sbin/xfs_io -r -c 'chproj 0' /proc/self/fd/3 2>&1; /usr/sbin/xfs_io -r -c 'chattr -P' /proc/self/fd/4 2>&1; echo CHILD_SHELL_DONE"}, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"})
	fmt.Println("CHILD_EXEC_FAILED", err)
	os.Exit(3)
}

func open(t *testing.T, path string, flags int) *os.File {
	t.Helper()
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return os.NewFile(uintptr(fd), path)
}

func attr(t *testing.T, f *os.File) fsxattr {
	t.Helper()
	a, err := getattr(f.Fd())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Prove runs after the fixture's Fresh bench is materialized and verified. It
// leaves the bench's tree, projects and flags exactly as it found them.
func Prove(t *testing.T, private payloadexec.Policy, storage hoststorage.Policy, key, member string, project uint32) {
	t.Helper()
	leafPath := filepath.Join(storage.Root, "benches", key)
	leaf := open(t, leafPath, syscall.O_RDONLY|syscall.O_DIRECTORY)
	defer leaf.Close()
	dir := open(t, filepath.Join(leafPath, member), syscall.O_RDONLY|syscall.O_DIRECTORY)
	defer dir.Close()
	file := open(t, filepath.Join(leafPath, member, "payload.txt"), syscall.O_RDONLY)
	defer file.Close()
	limit := int(storage.BenchInodes)
	if err := quotaleaf.Tree(leaf, project, limit); err != nil {
		t.Fatalf("materialized tree outside its project: %v", err)
	}
	marker("LOWER_CONFINE_TREE_CHECK_ACCEPTS_MATERIALIZED_TREE_PASS")

	// Control: without the filter the owner really can leave the project.
	scratch := filepath.Join(leafPath, member, ".confine-control")
	control := open(t, scratch, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL)
	if err := control.Chown(int(private.PayloadUID), int(private.PayloadGID)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/usr/sbin/xfs_io", "-r", "-c", "chproj 0", "/proc/self/fd/3")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.ExtraFiles = []*os.File{control}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: private.PayloadUID, Gid: private.PayloadGID, Groups: []uint32{}}}
	if out, err := cmd.CombinedOutput(); err != nil || attr(t, control).Projid != 0 {
		t.Fatalf("unconfined owner chproj control did not escape: %v %s", err, out)
	}
	marker("LOWER_CONFINE_UNCONFINED_OWNER_CHPROJ_ESCAPES_CONTROL_PASS")
	if err := quotaleaf.Tree(leaf, project, limit); !errors.Is(err, quotaleaf.ErrTreeProject) {
		t.Fatalf("tree check accepted a file outside its project: %v", err)
	}
	marker("LOWER_CONFINE_TREE_CHECK_REFUSES_ESCAPED_FILE_PASS")
	_ = control.Close()
	if err := syscall.Unlinkat(int(dir.Fd()), ".confine-control"); err != nil {
		t.Fatal(err)
	}
	if err := quotaleaf.Tree(leaf, project, limit); err != nil {
		t.Fatalf("tree after control removal: %v", err)
	}

	// The confined child: the production drop, then every escape route.
	policy, err := private.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/proc/self/exe", "-test.run=^$")
	child.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", childEnv + "=" + string(policy)}
	child.ExtraFiles = []*os.File{file, dir}
	out, err := child.CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("confined child: %v %s", err, text)
	}
	t.Logf("confined child output:\n%s", text)
	if a, d := attr(t, file), attr(t, dir); a.Projid != project || d.Projid != project || d.Xflags&quotaleaf.FlagProjInherit == 0 {
		t.Fatalf("confined payload changed project or inherit flag: %+v %+v", a, d)
	}
	for _, c := range []struct{ child, marker string }{
		{"CHILD_FSSETXATTR_PROJECT_EPERM\n", "LOWER_CONFINE_PAYLOAD_FSSETXATTR_PROJECT_REFUSED_PASS"},
		{"CHILD_FSSETXATTR_INHERIT_EPERM\n", "LOWER_CONFINE_PAYLOAD_FSSETXATTR_INHERIT_REFUSED_PASS"},
		{"CHILD_SETFLAGS_EPERM\n", "LOWER_CONFINE_PAYLOAD_SETFLAGS_REFUSED_PASS"},
		{"CHILD_IO_URING_SETUP_EPERM\n", "LOWER_CONFINE_PAYLOAD_IO_URING_REFUSED_PASS"},
	} {
		if !strings.Contains(text, c.child) {
			t.Fatalf("missing %q", c.child)
		}
		marker(c.marker)
	}
	if !strings.HasSuffix(text, "CHILD_SHELL_DONE\n") || strings.Count(text, "Operation not permitted") < 2 {
		t.Fatalf("xfs_io after exec was not refused twice: %s", text)
	}
	marker("LOWER_CONFINE_PAYLOAD_XFS_IO_CHPROJ_AND_CHATTR_REFUSED_AFTER_EXEC_PASS")

	// Root changes the leaf: the read-only verifier must refuse it.
	original := attr(t, leaf)
	refuse := func(changed fsxattr, name string) {
		if err := setattr(leaf.Fd(), changed); err != nil {
			t.Fatal(err)
		}
		_, err := storage.VerifyStorage(t.Context(), key)
		if e := setattr(leaf.Fd(), original); e != nil {
			t.Fatal(e)
		}
		if !errors.Is(err, quotaleaf.ErrLeafProject) {
			t.Fatalf("verify accepted %s: %v", name, err)
		}
		marker("LOWER_VERIFY_REFUSES_" + name + "_PASS")
	}
	moved := original
	moved.Projid = 0
	refuse(moved, "LEAF_PROJECT_CHANGED_BY_ROOT")
	cleared := original
	cleared.Xflags &^= quotaleaf.FlagProjInherit
	refuse(cleared, "LEAF_WITHOUT_INHERIT")
	if _, err := storage.VerifyStorage(t.Context(), key); err != nil {
		t.Fatalf("verify refused the restored leaf: %v", err)
	}
	marker("LOWER_VERIFY_ACCEPTS_RESTORED_LEAF_PASS")
}
