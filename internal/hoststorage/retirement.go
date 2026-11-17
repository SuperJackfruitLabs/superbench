package hoststorage

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const RetirementVersion = "superbench.storage-retirement.v1"
const MaxRetirementApproval = 65536

// RetirementRoot pins the fixed benches parent and either the approved final
// directory identity/mode or its explicit absence. It never names another path.
// This is historical binding only: retirement ALWAYS requires current leaf
// absence and NEVER removes a directory, even the originally approved one.
type RetirementRoot struct {
	Absent                    bool
	Device, Inode             uint64
	Mode                      uint32
	ParentDevice, ParentInode uint64
	ParentMode                uint32
}

// RetirementApproval must be retained in the caller's durable reviewed intent
// before use. It authorizes neither recursive deletion nor quota reassignment.
type RetirementApproval struct {
	Version, Key string
	Storage      StorageIdentity
	Root         RetirementRoot
}

func (a RetirementApproval) validate() error {
	if a.Version != RetirementVersion || a.Key != a.Storage.BenchKey || !a.Storage.Managed {
		return errors.New("invalid managed retirement approval")
	}
	if err := a.Storage.Validate(); err != nil {
		return err
	}
	r := a.Root
	if r.ParentDevice != a.Storage.Device || r.ParentInode == 0 || os.FileMode(r.ParentMode)&os.ModeType != os.ModeDir {
		return errors.New("invalid retirement parent identity")
	}
	if r.Absent {
		if r.Device != 0 || r.Inode != 0 || r.Mode != 0 {
			return errors.New("absence cannot claim a directory identity")
		}
	} else if r.Device != a.Storage.Device || r.Inode == 0 || os.FileMode(r.Mode)&os.ModeType != os.ModeDir {
		return errors.New("invalid retirement directory identity")
	}
	return nil
}
func (a RetirementApproval) Bytes() ([]byte, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(a)
	if len(raw) > MaxRetirementApproval {
		return nil, errors.New("retirement approval exceeds limit")
	}
	return raw, err
}
func ParseRetirementApproval(raw []byte) (RetirementApproval, error) {
	var a RetirementApproval
	if len(raw) > MaxRetirementApproval {
		return a, errors.New("retirement approval exceeds limit")
	}
	if err := strictStorageJSON(raw, &a); err != nil {
		return RetirementApproval{}, err
	}
	canonical, err := a.Bytes()
	if err != nil {
		return RetirementApproval{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return RetirementApproval{}, errors.New("retirement approval is not canonical")
	}
	return a, nil
}

// CaptureRetirementRoot only observes the fixed logical path. A reap caller that
// thaws the root must approve that exact final mode in its durable manifest.
func CaptureRetirementRoot(s StorageIdentity) (RetirementRoot, error) {
	var r RetirementRoot
	if err := s.Validate(); err != nil {
		return r, err
	}
	parent, err := openRetirementParent(s, nil, 0)
	if err != nil {
		return r, err
	}
	defer parent.Close()
	info, err := parent.Lstat(".")
	if err != nil {
		return r, err
	}
	st := info.Sys().(*syscall.Stat_t)
	r.ParentDevice, r.ParentInode, r.ParentMode = uint64(st.Dev), st.Ino, uint32(info.Mode())
	leaf, err := parent.Lstat(s.BenchKey)
	if errors.Is(err, syscall.ENOENT) {
		r.Absent = true
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if !leaf.IsDir() || leaf.Mode()&os.ModeSymlink != 0 {
		return r, errors.New("retirement root is not a physical directory")
	}
	st = leaf.Sys().(*syscall.Stat_t)
	r.Device, r.Inode, r.Mode = uint64(st.Dev), st.Ino, uint32(leaf.Mode())
	if r.Device != r.ParentDevice {
		return RetirementRoot{}, errors.New("retirement root is on another filesystem")
	}
	return r, nil
}

// retirementParent is the opened benches parent and this boot's data device.
type retirementParent struct {
	*os.Root
	device uint64
}

// openRetirementParent compares a managed identity's and a retained root's
// device numbers only with this boot's data root device: a reboot can renumber
// the data device, and the UUID binds the volume (Identity.Matches). A nonzero
// bound is the device a caller's own storage verification observed; the parent
// must be on that same filesystem.
func openRetirementParent(s StorageIdentity, expected *RetirementRoot, bound uint64) (retirementParent, error) {
	var none retirementParent
	device, inode, err := physicalStorageRoot(s.Root)
	if err != nil {
		return none, err
	}
	if device != s.Device && !s.Managed || bound != 0 && device != bound || inode != s.Inode {
		return none, errors.New("retirement data root identity changed")
	}
	path := filepath.Join(s.Root, "benches")
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return none, err
	}
	if physical != path {
		return none, errors.New("retirement parent contains a symlink")
	}
	parent, err := os.OpenRoot(path)
	if err != nil {
		return none, err
	}
	info, err := parent.Lstat(".")
	if err != nil {
		parent.Close()
		return none, err
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || uint64(st.Dev) != device {
		parent.Close()
		return none, errors.New("retirement parent filesystem changed")
	}
	if expected != nil && (uint64(st.Dev) != expected.ParentDevice && !s.Managed || st.Ino != expected.ParentInode || uint32(info.Mode()) != expected.ParentMode) {
		parent.Close()
		return none, errors.New("retirement parent identity changed")
	}
	return retirementParent{parent, device}, nil
}

// checkRetirementRoot distinguishes leaf ENOENT inside an already opened exact
// parent from missing ancestors, symlinks, ENOTDIR and permission failures.
func checkRetirementRoot(parent retirementParent, a RetirementApproval, requireAbsent bool) (bool, error) {
	info, err := parent.Lstat(a.Key)
	if errors.Is(err, syscall.ENOENT) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if requireAbsent || a.Root.Absent {
		return false, errors.New("retirement root reappeared; preserve it")
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || uint64(st.Dev) != parent.device || st.Ino != a.Root.Inode || uint32(info.Mode()) != a.Root.Mode {
		return false, errors.New("approved retirement root was replaced or changed")
	}
	return false, nil
}
