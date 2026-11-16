package repaircontrol

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const identityName = "repair-operations.identity"

type fileIdentity struct {
	Device, Inode  uint64
	Mode, UID, GID uint32
}
type authority struct {
	Instance                        string
	State, Database, Lock, Identity fileIdentity
}

func physicalID(info os.FileInfo) fileIdentity {
	st := info.Sys().(*syscall.Stat_t)
	return fileIdentity{Device: uint64(st.Dev), Inode: st.Ino, Mode: uint32(info.Mode()), UID: st.Uid, GID: st.Gid}
}

// The fixed anchor is checked before SQLite may recover/write an existing
// journal. Copying database bytes or replacing the admission lock never creates
// a second authority domain around an older still-open descriptor.
func (j *Journal) openAnchor(fresh bool, expected string) error {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if fresh {
		flags = os.O_RDWR | os.O_CREATE | os.O_EXCL | syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(filepath.Join(j.state, identityName), flags, 0600)
	if err != nil {
		return err
	}
	j.anchor = f
	if fresh && os.Geteuid() == 0 {
		if err := f.Chown(int(j.owner), int(j.directory.Sys().(*syscall.Stat_t).Gid)); err != nil {
			return err
		}
	}
	j.anchorIdentity, err = privateRegular(f, j.owner)
	if err != nil {
		return err
	}
	a := authority{State: physicalID(j.directory), Database: physicalID(j.database), Lock: physicalID(j.lockIdentity), Identity: physicalID(j.anchorIdentity)}
	if fresh {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		a.Instance = hex.EncodeToString(nonce[:])
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		n, err := f.Write(raw)
		if err != nil {
			return err
		}
		if n != len(raw) {
			return io.ErrShortWrite
		}
		if err := f.Sync(); err != nil {
			return err
		}
		if err := syncDirectory(j.state); err != nil {
			return err
		}
	}
	raw, err := io.ReadAll(io.NewSectionReader(f, 0, 4097))
	if err != nil {
		return err
	}
	if len(raw) > 4096 {
		return ErrAuthority
	}
	var stored authority
	if json.Unmarshal(raw, &stored) != nil || !id32.MatchString(stored.Instance) || expected != "" && stored.Instance != expected {
		return ErrAuthority
	}
	a.Instance = stored.Instance
	canonical, err := json.Marshal(a)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ErrAuthority
	}
	j.instance = stored.Instance
	j.authority = string(raw)
	return nil
}

func (j *Journal) checkAnchor() error {
	raw, err := io.ReadAll(io.NewSectionReader(j.anchor, 0, 4097))
	if err != nil {
		return err
	}
	if string(raw) != j.authority {
		return ErrAuthority
	}
	return nil
}
