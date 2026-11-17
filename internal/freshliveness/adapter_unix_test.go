//go:build linux || darwin

package freshliveness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeHost is a cgroup root and a proc root made of plain directories and
// symlinks into a bench directory, as the adapter reads the real ones.
type fakeHost struct {
	cgroup, proc, bench string
	device              uint64
}

func newFakeHost(t *testing.T) fakeHost {
	t.Helper()
	dir := t.TempDir()
	h := fakeHost{cgroup: filepath.Join(dir, "cgroup"), proc: filepath.Join(dir, "proc"), bench: filepath.Join(dir, "bench")}
	for _, d := range []string{h.cgroup, h.proc, filepath.Join(h.bench, "api")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var st unix.Stat_t
	if err := unix.Stat(h.bench, &st); err != nil {
		t.Fatal(err)
	}
	h.device = uint64(st.Dev)
	return h
}

// process adds pid with one task whose cwd is cwd, running as uid.
func (h fakeHost) process(t *testing.T, pid string, uid int, cwd string) {
	t.Helper()
	task := filepath.Join(h.proc, pid, "task", pid)
	for _, d := range []string{filepath.Join(task, "fd"), filepath.Join(task, "map_files")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.proc, pid, "status"), []byte("Name:\tsleep\nUid:\t"+strconv.Itoa(uid)+"\t"+strconv.Itoa(uid)+"\t"+strconv.Itoa(uid)+"\t"+strconv.Itoa(uid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{"cwd", "root"} {
		target := "/"
		if link == "cwd" {
			target = cwd
		}
		if err := os.Symlink(target, filepath.Join(task, link)); err != nil {
			t.Fatal(err)
		}
	}
}

// scope adds a populated scope, nested at path, holding pid.
func (h fakeHost) scope(t *testing.T, path, pid string) {
	t.Helper()
	dir := filepath.Join(h.cgroup, path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.events"), []byte("populated 1\nfrozen 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(pid+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// adapter attributes project 10007 to exactly the entries in the bench, as XFS
// project inheritance does; anything else on the device has project 0.
func (h fakeHost) adapter() adapter {
	project := func(fd int) (uint32, error) {
		var held unix.Stat_t
		if err := unix.Fstat(fd, &held); err != nil {
			return 0, err
		}
		found := uint32(0)
		_ = filepath.WalkDir(h.bench, func(path string, _ os.DirEntry, err error) error {
			var st unix.Stat_t
			if err == nil && unix.Lstat(path, &st) == nil && st.Ino == held.Ino {
				found = 10007
			}
			return nil
		})
		return found, nil
	}
	return adapter{cgroupRoot: h.cgroup, proc: h.proc, self: -1, payloadUID: 4321, dataDevice: h.device, project: 10007, projectOf: project}
}

// The scope path alone: a non-payload process in a nested populated scope with
// its working directory in the bench.
func TestInstalledSeesAProcessInANestedScope(t *testing.T) {
	h := newFakeHost(t)
	h.process(t, "1200", 1000, filepath.Join(h.bench, "api"))
	h.scope(t, "0123456789abcdef0123456789abcdef/inner", "1200")
	if err := h.adapter().liveness().Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("nested scope missed: %v", err)
	}
	a := h.adapter()
	a.project = 10008
	if err := a.liveness().Idle(); err != nil {
		t.Fatalf("another project's use counted: %v", err)
	}
}

// The payload path alone: a payload process in no scope, using the bench
// through one of its descriptors.
func TestInstalledSeesAPayloadProcessOutsideTheScopes(t *testing.T) {
	h := newFakeHost(t)
	h.process(t, "1300", 4321, "/")
	if err := h.adapter().liveness().Idle(); err != nil {
		t.Fatalf("an idle payload process counted: %v", err)
	}
	file := filepath.Join(h.bench, "api", "open")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, filepath.Join(h.proc, "1300", "task", "1300", "fd", "3")); err != nil {
		t.Fatal(err)
	}
	if err := h.adapter().liveness().Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("payload descriptor missed: %v", err)
	}
}

// Every task counts, a mapping counts, and a process whose tasks cannot be
// read counts as use.
func TestInstalledScansEveryTaskAndMapping(t *testing.T) {
	h := newFakeHost(t)
	h.process(t, "1400", 4321, "/")
	other := filepath.Join(h.proc, "1400", "task", "1401")
	if err := os.MkdirAll(filepath.Join(other, "fd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(other, "map_files"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(other, "root")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.bench, "api"), filepath.Join(other, "cwd")); err != nil {
		t.Fatal(err)
	}
	if err := h.adapter().liveness().Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("a non-leader task missed: %v", err)
	}
	if err := os.Remove(filepath.Join(other, "cwd")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(other, "cwd")); err != nil {
		t.Fatal(err)
	}
	mapped := filepath.Join(h.bench, "api", "lib.so")
	if err := os.WriteFile(mapped, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mapped, filepath.Join(other, "map_files", "7f00-7f01")); err != nil {
		t.Fatal(err)
	}
	if err := h.adapter().liveness().Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("a mapping missed: %v", err)
	}
	if err := os.Remove(filepath.Join(other, "map_files", "7f00-7f01")); err != nil {
		t.Fatal(err)
	}
	if err := h.adapter().liveness().Idle(); err != nil {
		t.Fatalf("idle again: %v", err)
	}
	if err := os.Chmod(filepath.Join(other, "fd"), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(other, "fd"), 0o700)
	if os.Getuid() != 0 {
		if err := h.adapter().liveness().Idle(); !errors.Is(err, ErrBusy) {
			t.Fatalf("an unreadable task counted as idle: %v", err)
		}
	}
}

// Without a readable map_files, the task's other entries still count, and
// a mapping of a file on the data device in its maps counts too: map_files
// unreadable (here a non-directory, which root cannot read as one either), or
// absent on a kernel without CONFIG_CHECKPOINT_RESTORE.
func TestInstalledWithoutMapFilesChecksEntriesThenMaps(t *testing.T) {
	setMaps := func(t *testing.T, task string, device uint64) {
		t.Helper()
		line := fmt.Sprintf("7f00-7f01 r-xp 00000000 %02x:%02x 99 /srv/data/benches/card/lib.so\n", unix.Major(device), unix.Minor(device))
		if err := os.WriteFile(filepath.Join(task, "maps"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// (a) map_files unreadable while the task's cwd is in the bench.
	h := newFakeHost(t)
	h.process(t, "1500", 4321, filepath.Join(h.bench, "api"))
	task := filepath.Join(h.proc, "1500", "task", "1500")
	if err := os.Remove(filepath.Join(task, "map_files")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(task, "map_files"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setMaps(t, task, h.device+1)
	if err := h.adapter().liveness().Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("unreadable map_files hid the cwd: %v", err)
	}
	// (b) map_files absent while maps holds a mapping on the data device.
	h = newFakeHost(t)
	h.process(t, "1600", 4321, "/")
	task = filepath.Join(h.proc, "1600", "task", "1600")
	if err := os.Remove(filepath.Join(task, "map_files")); err != nil {
		t.Fatal(err)
	}
	setMaps(t, task, h.device+1)
	if err := h.adapter().liveness().Idle(); err != nil {
		t.Fatalf("another device's mapping counted: %v", err)
	}
	setMaps(t, task, h.device)
	if err := h.adapter().liveness().Idle(); !errors.Is(err, ErrBusy) {
		t.Fatalf("absent map_files skipped maps: %v", err)
	}
}
