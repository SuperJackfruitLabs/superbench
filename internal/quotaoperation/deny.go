package quotaoperation

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type denialIO struct {
	write func(*os.File, []byte, int64) (int, error)
	sync  func(*os.File) error
}

// The immutable canonical identity is never rewritten. State is one byte: 0
// admits fixed-policy effects; 1 permanently denies them. A failed/torn update
// cannot produce proof. Exact retry syncs even an already observed 1, then
// rechecks the same inode and canonical bytes before acknowledging durability.
func denyMarker(path string, expected marker, uid uint32, check func() error, ops denialIO) error {
	if err := check(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	original, err := f.Stat()
	if err != nil {
		return err
	}
	if !privateFile(original, uid) || original.Size() > maxMarker || expected.info == nil || !os.SameFile(expected.info, original) {
		return ErrAuthority
	}
	same := func() error {
		if err := check(); err != nil {
			return err
		}
		named, err := os.Lstat(path)
		if err != nil || !os.SameFile(original, named) || !privateFile(named, uid) {
			return ErrAuthority
		}
		return nil
	}
	if err = same(); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxMarker+1))
	if err != nil {
		return err
	}
	if len(raw) > maxMarker {
		return ErrAuthority
	}
	var m marker
	if err = json.Unmarshal(raw, &m); err != nil {
		return ErrAuthority
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(raw, canonical) || m.Version != Version || !m.Identity.valid() || (m.State != "0" && m.State != "1") {
		return ErrAuthority
	}
	if !equalMarker(m, expected) {
		return ErrAuthority
	}
	if m.State == "0" {
		needle := []byte(`"State":"0"`)
		if bytes.Count(raw, needle) != 1 {
			return ErrAuthority
		}
		offset := int64(bytes.Index(raw, needle) + len(`"State":"`))
		write := ops.write
		if write == nil {
			write = func(f *os.File, b []byte, offset int64) (int, error) { return f.WriteAt(b, offset) }
		}
		n, err := write(f, []byte("1"), offset)
		if err != nil {
			return err
		}
		if n != 1 {
			return io.ErrShortWrite
		}
	}
	sync := ops.sync
	if sync == nil {
		sync = func(f *os.File) error { return f.Sync() }
	}
	if err = sync(f); err != nil {
		return err
	}
	if err = same(); err != nil {
		return err
	}
	got, err := readMarker(path, uid)
	if err != nil {
		return err
	}
	m.State = "1"
	if !equalMarker(got, m) {
		return ErrAuthority
	}
	if err = syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return same()
}
