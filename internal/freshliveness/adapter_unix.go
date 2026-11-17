//go:build linux || darwin

package freshliveness

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// maxCgroups bounds the scopes one check walks beneath the cgroup root.
const maxCgroups = 1 << 16

// adapter reads one bench's users from a cgroup root and a proc root.
type adapter struct {
	cgroupRoot, proc string
	self             int
	payloadUID       uint32
	dataDevice       uint64
	project          uint32
	projectOf        func(fd int) (uint32, error)
}

func gone(err error) bool { return errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) }

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

func (a adapter) liveness() Liveness {
	return Liveness{Scopes: a.scopes, Events: a.events, Members: a.members, Processes: a.processes, Payload: a.payload, Uses: a.uses}
}

func (a adapter) scopes() ([]string, error) {
	var names []string
	err := filepath.WalkDir(a.cgroupRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if gone(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() || path == a.cgroupRoot {
			return nil
		}
		if len(names) == maxCgroups {
			return fmt.Errorf("%w: too many cgroups to check", ErrBusy)
		}
		rel, err := filepath.Rel(a.cgroupRoot, path)
		if err != nil {
			return err
		}
		names = append(names, rel)
		return nil
	})
	return names, err
}

func (a adapter) events(scope string) ([]byte, error) {
	raw, err := readBounded(filepath.Join(a.cgroupRoot, scope, "cgroup.events"), 4096)
	if gone(err) {
		return []byte("populated 0\n"), nil
	}
	return raw, err
}

func (a adapter) members(scope string) ([]int, error) {
	raw, err := readBounded(filepath.Join(a.cgroupRoot, scope, "cgroup.procs"), 1<<20)
	if gone(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, f := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			return nil, ErrBusy
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

func (a adapter) processes() ([]int, error) {
	entries, err := os.ReadDir(a.proc)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid != a.self {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func (a adapter) payload(pid int) (bool, error) {
	raw, err := readBounded(filepath.Join(a.proc, strconv.Itoa(pid), "status"), 64<<10)
	if gone(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want := strconv.FormatUint(uint64(a.payloadUID), 10)
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && key == "Uid" {
			for _, id := range strings.Fields(value) {
				if id == want {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// uses scans every task of pid. A process whose task list or a task's
// descriptors cannot be read while it still has tasks counts as use.
func (a adapter) uses(pid int) (bool, error) {
	if pid == a.self {
		return false, nil
	}
	tasks, err := os.ReadDir(filepath.Join(a.proc, strconv.Itoa(pid), "task"))
	if gone(err) {
		return false, nil
	}
	if err != nil || len(tasks) == 0 {
		return true, nil
	}
	for _, t := range tasks {
		using, err := a.task(filepath.Join(a.proc, strconv.Itoa(pid), "task", t.Name()))
		if err != nil || using {
			return true, nil
		}
	}
	return false, nil
}

// task checks one task's cwd, root, descriptors and file mappings. A task
// that vanished is not use; unreadable descriptors are an error. Without a
// readable map_files (unreadable, or absent on a kernel without
// CONFIG_CHECKPOINT_RESTORE), any mapping on the data device in the task's
// maps counts, after every other entry is checked.
func (a adapter) task(base string) (bool, error) {
	vanished := func() bool { _, err := os.Lstat(base); return gone(err) }
	paths := []string{filepath.Join(base, "cwd"), filepath.Join(base, "root")}
	fds, err := os.ReadDir(filepath.Join(base, "fd"))
	if err != nil {
		if gone(err) && vanished() {
			return false, nil
		}
		return false, err
	}
	for _, e := range fds {
		paths = append(paths, filepath.Join(base, "fd", e.Name()))
	}
	maps, err := os.ReadDir(filepath.Join(base, "map_files"))
	if err != nil && gone(err) && vanished() {
		return false, nil
	}
	for _, e := range maps {
		paths = append(paths, filepath.Join(base, "map_files", e.Name()))
	}
	for _, path := range paths {
		if using, err := a.entry(path); err != nil || using {
			return true, nil
		}
	}
	if err != nil {
		return a.mapsOnDevice(base)
	}
	return false, nil
}

// entry reports whether one /proc link resolves to a regular file or a
// directory on the data device in the bench's project.
func (a adapter) entry(path string) (bool, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil || uint64(st.Dev) != a.dataDevice {
		return false, nil
	}
	if kind := st.Mode & unix.S_IFMT; kind != unix.S_IFREG && kind != unix.S_IFDIR {
		return false, nil
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if gone(err) {
		return false, nil
	}
	if err != nil {
		return true, nil
	}
	defer unix.Close(fd)
	project, err := a.projectOf(fd)
	return err != nil || project == a.project, nil
}

// mapsOnDevice reads a task's maps: any mapping of a file on the data
// device is use.
func (a adapter) mapsOnDevice(base string) (bool, error) {
	raw, err := readBounded(filepath.Join(base, "maps"), 16<<20)
	if gone(err) {
		return false, nil
	}
	if err != nil {
		return true, nil
	}
	want := fmt.Sprintf("%02x:%02x", unix.Major(a.dataDevice), unix.Minor(a.dataDevice))
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) >= 5 && f[3] == want && f[4] != "0" {
			return true, nil
		}
	}
	return false, nil
}
