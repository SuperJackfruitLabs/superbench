// Package quotaoperation serializes typed fixed-helper invocations and durable
// operation deny barriers. It grants no allocation, path, cap or caller identity.
package quotaoperation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const Version = "superbench.quota-operation.v1"
const MaxMarkers = 10000
const MaxPending = MaxMarkers
const maxMarker = 2048

var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var ErrCancelled = errors.New("quota operation cancelled")
var ErrAuthority = errors.New("quota operation authority changed")
var ErrCapacity = errors.New("quota operation marker capacity exhausted")

type Identity struct{ OperationID, Epoch, StateIdentity, Authority, BinarySHA256 string }

func (i Identity) valid() bool {
	return hex64.MatchString(i.OperationID) && hex32.MatchString(i.Epoch) && hex64.MatchString(i.StateIdentity) && hex64.MatchString(i.Authority) && hex64.MatchString(i.BinarySHA256)
}

type Proof struct {
	Version   string
	Identity  Identity
	Cancelled bool
}
type marker struct {
	Version  string
	Identity Identity
	State    string
	info     os.FileInfo
}

type Store struct {
	Directory string
	// Check must independently verify the original fixed policy, mounted root,
	// privileged ledger/anchor and actual helper binary against the identity.
	// The fixed helper constructs this callback; none of these are caller paths.
	Check  func(Identity) error
	uid    uint32 // production is root; private native fixtures use their owner
	denyIO denialIO
}

func (s Store) Apply(i Identity, fn func() error) error {
	if fn == nil {
		return ErrAuthority
	}
	return s.within(i, func(dir string, current *marker, check func() error) error {
		if current != nil && current.State == "1" {
			return ErrCancelled
		}
		if current == nil {
			if err := saveMarker(dir, marker{Version: Version, Identity: i, State: "0"}, s.uid, check); err != nil {
				return err
			}
		}
		// Reconfirm durability even when reusing an intent left by an interrupted
		// helper whose final directory sync may not have returned to its controller.
		if err := syncDir(dir); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
		return fn()
	})
}
func (s Store) Cancel(i Identity) (Proof, error) {
	var proof Proof
	err := s.within(i, func(dir string, current *marker, check func() error) error {
		if current == nil {
			if err := saveMarker(dir, marker{Version: Version, Identity: i, State: "1"}, s.uid, check); err != nil {
				return err
			}
		}
		if current == nil {
			m, err := readMarker(filepath.Join(dir, i.OperationID+".json"), s.uid)
			if err != nil {
				return err
			}
			current = &m
		}
		if err := denyMarker(filepath.Join(dir, i.OperationID+".json"), *current, s.uid, check, s.denyIO); err != nil {
			return err
		}
		proof = Proof{Version: Version, Identity: i, Cancelled: true}
		return nil
	})
	if err != nil {
		return Proof{}, err
	}
	return proof, nil
}

func (s Store) within(i Identity, fn func(string, *marker, func() error) error) error {
	if uint32(os.Geteuid()) != s.uid || s.Check == nil || !i.valid() || !filepath.IsAbs(s.Directory) || filepath.Clean(s.Directory) != s.Directory {
		return ErrAuthority
	}
	if err := s.Check(i); err != nil {
		return err
	}
	parent, err := directory(s.Directory, s.uid)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(s.Directory, "operations.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	if !privateFile(info, s.uid) {
		return ErrAuthority
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	recheckParent := func() error {
		now, e := directory(s.Directory, s.uid)
		if e != nil {
			return e
		}
		if !os.SameFile(parent, now) {
			return ErrAuthority
		}
		named, e := os.Lstat(filepath.Join(s.Directory, "operations.lock"))
		if e != nil || !os.SameFile(info, named) {
			return ErrAuthority
		}
		return s.Check(i)
	}
	if err = recheckParent(); err != nil {
		return err
	}
	dir := filepath.Join(s.Directory, "operations")
	if err = os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err = syncDir(s.Directory); err != nil {
		return err
	}
	root, err := directory(dir, s.uid)
	if err != nil {
		return err
	}
	check := func() error {
		if e := recheckParent(); e != nil {
			return e
		}
		now, e := directory(dir, s.uid)
		if e != nil {
			return e
		}
		if !os.SameFile(root, now) {
			return ErrAuthority
		}
		return nil
	}
	current, count, reserved, err := inventory(dir, i, s.uid)
	if err != nil {
		return err
	}
	if current == nil && !reserved && count >= MaxMarkers {
		return ErrCapacity
	}
	if err = check(); err != nil {
		return err
	}
	return fn(dir, current, check)
}
func inventory(dir string, i Identity, uid uint32) (*marker, int, bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	var selected *marker
	identities := map[string]Identity{}
	pending := 0
	for {
		entries, readErr := f.ReadDir(128)
		if readErr != nil && readErr != io.EOF {
			return nil, 0, false, readErr
		}
		for _, entry := range entries {
			name := entry.Name()
			id, epoch, isPending := pendingName(name)
			if isPending {
				pending++
				if pending > MaxPending {
					return nil, 0, false, ErrCapacity
				}
			} else {
				if len(name) != 69 || name[64:] != ".json" || !hex64.MatchString(name[:64]) {
					return nil, 0, false, ErrAuthority
				}
				id = name[:64]
			}
			m, err := readMarker(filepath.Join(dir, name), uid)
			if err != nil {
				return nil, 0, false, err
			}
			if m.Identity.OperationID != id || isPending && m.Identity.Epoch != epoch {
				return nil, 0, false, ErrAuthority
			}
			if prior, ok := identities[id]; ok && prior != m.Identity {
				return nil, 0, false, ErrAuthority
			}
			identities[id] = m.Identity
			if len(identities) > MaxMarkers {
				return nil, 0, false, ErrCapacity
			}
			if id == i.OperationID {
				if m.Identity != i {
					return nil, 0, false, ErrAuthority
				}
				if !isPending {
					copy := m
					selected = &copy
				}
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	_, reserved := identities[i.OperationID]
	return selected, len(identities), reserved, nil
}
func pendingName(name string) (string, string, bool) {
	parts := strings.Split(name, "-")
	if len(parts) != 4 || parts[0] != "pending" || !hex64.MatchString(parts[1]) || !hex32.MatchString(parts[2]) || len(parts[3]) < 1 || len(parts[3]) > 32 {
		return "", "", false
	}
	for _, r := range parts[3] {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return parts[1], parts[2], true
}

func directory(path string, uid uint32) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || physical != path || !info.IsDir() || info.Mode().Perm() != 0700 || st.Uid != uid {
		return nil, ErrAuthority
	}
	return info, nil
}
func privateFile(info os.FileInfo, uid uint32) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Mode().Perm() == 0600 && st.Uid == uid && st.Nlink == 1
}
func readMarker(path string, uid uint32) (marker, error) {
	var m marker
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return m, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return m, err
	}
	if !privateFile(info, uid) || info.Size() > maxMarker {
		return m, ErrAuthority
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxMarker+1))
	if err != nil {
		return m, err
	}
	if len(raw) > maxMarker {
		return m, ErrAuthority
	}
	if err = json.Unmarshal(raw, &m); err != nil {
		return m, ErrAuthority
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(raw, canonical) || m.Version != Version || !m.Identity.valid() || (m.State != "0" && m.State != "1") {
		return m, ErrAuthority
	}
	m.info = info
	return m, nil
}
func equalMarker(a, b marker) bool {
	return a.Version == b.Version && a.Identity == b.Identity && a.State == b.State
}
func saveMarker(dir string, m marker, uid uint32, check func() error) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(raw) > maxMarker {
		return ErrAuthority
	}
	if err = check(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	pending := 0
	for _, entry := range entries {
		if _, _, ok := pendingName(entry.Name()); ok {
			pending++
		}
	}
	if pending >= MaxPending {
		return ErrCapacity
	}
	f, err := os.CreateTemp(dir, "pending-"+m.Identity.OperationID+"-"+m.Identity.Epoch+"-")
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = check(); err != nil {
		return err
	}
	path := filepath.Join(dir, m.Identity.OperationID+".json")
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	saved, err := readMarker(path, uid)
	if err != nil {
		return err
	}
	if !equalMarker(saved, m) {
		return ErrAuthority
	}
	return check()
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
