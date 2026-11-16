//go:build linux

package serviceidentity

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

type directoryIdentity struct {
	Device, Inode  uint64
	UID, GID, Mode uint32
}

type fileIdentity struct {
	directoryIdentity
	Links             uint64
	Size              int64
	Modified, Changed syscall.Timespec
}

type snapshot struct {
	Directories []directoryIdentity
	File        fileIdentity
	Canonical   string
}

func identity(s *syscall.Stat_t) directoryIdentity {
	return directoryIdentity{uint64(s.Dev), s.Ino, s.Uid, s.Gid, s.Mode}
}

func fileID(s *syscall.Stat_t) fileIdentity {
	return fileIdentity{identity(s), uint64(s.Nlink), s.Size, s.Mtim, s.Ctim}
}

func stat(file *os.File) (syscall.Stat_t, error) {
	var s syscall.Stat_t
	err := syscall.Fstat(int(file.Fd()), &s)
	return s, err
}

func directoryID(file *os.File) (directoryIdentity, error) {
	s, err := stat(file)
	if err != nil || s.Mode&syscall.S_IFMT != syscall.S_IFDIR || s.Uid != 0 || s.Mode&0022 != 0 {
		return directoryIdentity{}, errors.Join(ErrAuthority, err)
	}
	return identity(&s), nil
}

func openDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.Join(ErrAuthority, err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func openLeaf(parent *os.File, name string, owner uint32) (*os.File, fileIdentity, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fileIdentity{}, errors.Join(ErrAuthority, err)
	}
	file := os.NewFile(uintptr(fd), name)
	s, err := stat(file)
	if err != nil || s.Mode&syscall.S_IFMT != syscall.S_IFREG || s.Mode&07777 != 0644 || s.Uid != owner || s.Nlink != 1 || s.Size < 1 || s.Size > MaxRegistry {
		_ = file.Close()
		return nil, fileIdentity{}, errors.Join(ErrAuthority, err)
	}
	return file, fileID(&s), nil
}

// readInstalled keeps the complete physical chain held until bytes and named
// identities have been checked. No descriptor escapes the read. The only
// production caller supplies the fixed path; this is not a configurable API.
func readInstalled() (Registry, *snapshot, error) {
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Registry{}, nil, errors.Join(ErrAuthority, err)
	}
	held := []*os.File{os.NewFile(uintptr(fd), "/")}
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = held[i].Close()
		}
	}()
	parts := strings.Split(strings.TrimPrefix(filepath.Dir(FixedRegistry), "/"), "/")
	proof := &snapshot{}
	for i := 0; ; i++ {
		id, err := directoryID(held[i])
		if err != nil {
			return Registry{}, nil, err
		}
		proof.Directories = append(proof.Directories, id)
		if i == len(parts) {
			break
		}
		next, err := openDirectory(held[i], parts[i])
		if err != nil {
			return Registry{}, nil, err
		}
		held = append(held, next)
	}
	parent := held[len(held)-1]
	file, before, err := openLeaf(parent, filepath.Base(FixedRegistry), 0)
	if err != nil {
		return Registry{}, nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxRegistry+1))
	if err != nil {
		return Registry{}, nil, errors.Join(ErrAuthority, err)
	}
	after, err := stat(file)
	if err != nil || fileID(&after) != before || int64(len(raw)) != before.Size {
		return Registry{}, nil, errors.Join(ErrAuthority, err)
	}
	named, namedID, err := openLeaf(parent, filepath.Base(FixedRegistry), 0)
	if err != nil {
		return Registry{}, nil, err
	}
	_ = named.Close()
	if namedID != before {
		return Registry{}, nil, ErrAuthority
	}
	// Reopen names from the held parents, including the root. A replacement
	// anywhere in the chain cannot rebind this snapshot to another installation.
	for i, original := range held {
		var current *os.File
		if i == 0 {
			root, openErr := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			if openErr != nil {
				return Registry{}, nil, errors.Join(ErrAuthority, openErr)
			}
			current = os.NewFile(uintptr(root), "/")
		} else {
			current, err = openDirectory(held[i-1], parts[i-1])
			if err != nil {
				return Registry{}, nil, err
			}
		}
		id, checkErr := directoryID(current)
		_ = current.Close()
		originalID, originalErr := directoryID(original)
		if checkErr != nil || originalErr != nil || id != proof.Directories[i] || originalID != id {
			return Registry{}, nil, errors.Join(ErrAuthority, checkErr, originalErr)
		}
	}
	r, err := ParseRegistry(raw)
	if err != nil {
		return Registry{}, nil, err
	}
	canonical, err := r.Bytes()
	if err != nil {
		return Registry{}, nil, err
	}
	proof.File, proof.Canonical = before, string(canonical)
	return r, proof, nil
}

func serviceCredentials(p payloadpolicy.Policy, uid, euid, gid, egid int) error {
	if p.Validate() != nil || uid <= 0 || euid <= 0 || gid <= 0 || egid <= 0 || uint64(uid) != uint64(p.DaemonUID) || uint64(euid) != uint64(p.DaemonUID) || uint64(gid) != uint64(p.DaemonGID) || uint64(egid) != uint64(p.DaemonGID) {
		return ErrAuthority
	}
	return nil
}

// ResolveService always requires the complete installed binding. Missing or
// unreadable authority never selects an ordinary/cooperative execution mode.
func ResolveService() (Authority, error) {
	r, proof, err := readInstalled()
	if err != nil {
		return Authority{}, err
	}
	if err := serviceCredentials(r.Policy, os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid()); err != nil {
		return Authority{}, err
	}
	return Authority{registry: r, physical: proof}, nil
}

func (a Authority) Recheck() error {
	if a.physical == nil {
		return ErrAuthority
	}
	if err := serviceCredentials(a.registry.Policy, os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid()); err != nil {
		return err
	}
	r, proof, err := readInstalled()
	if err != nil {
		return err
	}
	if r != a.registry || !reflect.DeepEqual(proof, a.physical) {
		return ErrAuthority
	}
	return nil
}
