package maintenance

import (
	"bytes"
	"encoding/json"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/retention"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func missing(e error) bool { return os.IsNotExist(e) }
func (m Manager) read(name string, value any) error {
	if e := retention.Directory(m.Root); e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(m.Root, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Size() > reap.MaxManifest {
		return errors.New("maintenance state must be a bounded private owned regular file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, reap.MaxManifest+1))
	if e != nil {
		return e
	}
	if e = reap.CheckJSON(raw); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(value)
}
func (m Manager) write(name string, value any) error {
	if e := retention.Directory(m.Root); e != nil {
		return e
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if len(raw) > reap.MaxManifest {
		return errors.New("maintenance state exceeds limit")
	}
	path := filepath.Join(m.Root, name)
	if info, e := os.Lstat(path); e == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			return errors.New("refusing unsafe maintenance state replacement")
		}
	} else if !missing(e) {
		return e
	}
	f, e := os.CreateTemp(m.Root, "maintenance-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), path); e != nil {
		return e
	}
	return m.sync()
}
func (m Manager) sync() error {
	dir, e := os.Open(m.Root)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func (m Manager) clearPending() error {
	if e := os.Remove(filepath.Join(m.Root, "retention-auto-pending.json")); e != nil && !missing(e) {
		return e
	}
	return m.sync()
}
func (m Manager) gate(nonblocking bool) (func(), error) {
	if e := retention.Directory(m.Root); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(m.Root, "retention-policy.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, errors.New("unsafe retention policy lock")
	}
	flags := syscall.LOCK_EX
	if nonblocking {
		flags |= syscall.LOCK_NB
	}
	if e = syscall.Flock(int(f.Fd()), flags); e != nil {
		f.Close()
		return nil, e
	}
	return func() { f.Close() }, nil
}
func (m Manager) Status() (Status, error) {
	var out Status
	var c Config
	if e := m.read("retention-policy.json", &c); e == nil {
		if e = c.Proposal.Validate(m.Root); e != nil {
			return out, e
		}
		out.Config = &c
	} else if !missing(e) {
		return out, e
	}
	var p Pending
	if e := m.read("retention-auto-pending.json", &p); e == nil {
		if e = p.Plan.Validate(m.Root); e != nil {
			return out, e
		}
		out.Pending = &p
	} else if !missing(e) {
		return out, e
	}
	var r Report
	if e := m.read("retention-last.json", &r); e == nil {
		out.Last = &r
	} else if !missing(e) {
		return out, e
	}
	return out, nil
}
