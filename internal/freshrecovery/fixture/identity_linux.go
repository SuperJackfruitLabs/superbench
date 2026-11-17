//go:build linux && freshstoragefixture

package fixture

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// RequireUnusedIdentities proves the lower fixture's numeric identities
// (61001-61004) unassigned before the fixture uses them. This is an
// original-host, trusted-root test precondition, not a reservation against a
// concurrent administrator creating accounts or processes. It lives here, in
// the fixture-only package, beside the recovery and reclaim cases it guards.
func RequireUnusedIdentities() error {
	for id := 61001; id <= 61004; id++ {
		text := strconv.Itoa(id)
		if _, err := user.LookupId(text); err == nil {
			return fmt.Errorf("fixture UID already assigned")
		} else if _, ok := err.(user.UnknownUserIdError); !ok {
			return err
		}
		if _, err := user.LookupGroupId(text); err == nil {
			return fmt.Errorf("fixture GID already assigned")
		} else if _, ok := err.(user.UnknownGroupIdError); !ok {
			return err
		}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.ParseUint(entry.Name(), 10, 32); err != nil {
			continue
		}
		path := "/proc/" + entry.Name()
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			if lowerProcessGone(path) {
				continue
			}
			return err
		}
		dir := os.NewFile(uintptr(fd), path)
		err = inspectLowerProcess(dir)
		pinned, statErr := dir.Stat()
		named, nameErr := os.Stat(path)
		dir.Close()
		if lowerProcessGone(path) {
			continue
		}
		if err != nil {
			return err
		}
		if statErr != nil || nameErr != nil || !os.SameFile(pinned, named) {
			return fmt.Errorf("fixture process identity changed")
		}
	}
	return nil
}

func lowerProcessGone(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func inspectLowerProcess(dir *os.File) error {
	fd, err := syscall.Openat(int(dir.Fd()), "status", syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "process-status")
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("fixture process status unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return fmt.Errorf("fixture process status too large")
	}
	return lowerCredentialInventory(data)
}

func lowerCredentialInventory(data []byte) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || (key != "Uid" && key != "Gid" && key != "Groups") {
			continue
		}
		if seen[key] {
			return fmt.Errorf("duplicate fixture process credentials")
		}
		seen[key] = true
		fields := strings.Fields(value)
		if key != "Groups" && len(fields) != 4 {
			return fmt.Errorf("malformed fixture process credentials")
		}
		for _, field := range fields {
			id, err := strconv.ParseUint(field, 10, 32)
			if err != nil {
				return fmt.Errorf("malformed fixture process credentials")
			}
			if id >= 61001 && id <= 61004 {
				return fmt.Errorf("fixture identity already used by process")
			}
		}
	}
	if len(seen) != 3 {
		return fmt.Errorf("missing fixture process credentials")
	}
	return nil
}
