//go:build linux

package registeredinspection

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
)

type heldInput struct {
	parent, file *os.File
	name         string
	info         os.FileInfo
}

// InputObservation retains original file/directory descriptors. It is a bounded
// read observation, not writer quiescence, a preparation receipt or authority.
type InputObservation struct {
	closed                   bool
	gid                      uint32
	value                    Inputs
	checkout, config         *os.File
	checkoutInfo, configInfo os.FileInfo
	files                    []heldInput
	owned                    []*os.File
	uid                      uint32
	device                   uint64
}

func (o *InputObservation) Close() {
	for _, f := range o.owned {
		_ = f.Close()
	}
	o.closed = true
	o.owned = nil
	o.files = nil
}
func (o *InputObservation) Snapshot() Inputs {
	v := o.value
	v.Config.Data = append([]byte(nil), v.Config.Data...)
	v.Manifests = append([]InputFile(nil), v.Manifests...)
	return v
}
func openInput(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "registered preparation input"), nil
}
func (o *InputObservation) read(parent *os.File, name, wire string, max int64, data bool) (InputFile, error) {
	v := InputFile{Name: wire}
	f, err := openInput(parent, name)
	if errors.Is(err, syscall.ENOENT) {
		o.files = append(o.files, heldInput{parent: parent, name: name})
		return v, nil
	}
	if err != nil {
		return v, errors.Join(ErrBoundary, err)
	}
	o.owned = append(o.owned, f)
	before, err := f.Stat()
	if err != nil {
		return v, errors.Join(ErrBoundary, err)
	}
	st := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > max || st.Uid != o.uid || st.Gid != o.gid || st.Nlink != 1 || uint64(st.Dev) != o.device {
		return v, ErrBoundary
	}
	h := sha256.New()
	var count int64
	if data {
		v.Data, err = io.ReadAll(io.LimitReader(f, max+1))
		count = int64(len(v.Data))
		_, _ = h.Write(v.Data)
	} else {
		count, err = io.Copy(h, io.LimitReader(f, max+1))
	}
	after, e := f.Stat()
	if err != nil || e != nil || count > max || count != before.Size() || !SamePolicyFile(before, after) {
		return v, errors.Join(ErrBoundary, err, e)
	}
	v.Present, v.Bytes, v.SHA256 = true, uint64(count), hex.EncodeToString(h.Sum(nil))
	o.files = append(o.files, heldInput{parent: parent, file: f, name: name, info: before})
	return v, nil
}
func readInputs(checkout *os.File, member string, uid, gid uint32, device uint64) (_ *InputObservation, err error) {
	if checkout == nil || !memberName.MatchString(member) || (uid == 0 || gid == 0) {
		return nil, ErrBoundary
	}
	o := &InputObservation{checkout: checkout, uid: uid, gid: gid, device: device, value: Inputs{Version: InputsVersion, Member: member}}
	defer func() {
		if err != nil {
			o.Close()
		}
	}()
	o.checkoutInfo, err = checkout.Stat()
	if err != nil {
		return nil, errors.Join(ErrBoundary, err)
	}
	o.value.Config = InputFile{Name: ".bench/config.toml"}
	fd, e := syscall.Openat(int(checkout.Fd()), ".bench", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	switch {
	case errors.Is(e, syscall.ENOENT):
	case e != nil:
		return nil, errors.Join(ErrBoundary, e)
	default:
		o.config = os.NewFile(uintptr(fd), "registered config directory")
		o.owned = append(o.owned, o.config)
		o.configInfo, err = o.config.Stat()
		if err != nil {
			return nil, errors.Join(ErrBoundary, err)
		}
		st := o.configInfo.Sys().(*syscall.Stat_t)
		if !o.configInfo.IsDir() || st.Uid != uid || st.Gid != gid || uint64(st.Dev) != device || o.configInfo.Mode().Perm()&0022 != 0 {
			return nil, ErrBoundary
		}
		o.value.Config, err = o.read(o.config, "config.toml", ".bench/config.toml", MaxConfigBytes, true)
		if err != nil {
			return nil, err
		}
	}
	for _, name := range dependencyNames {
		f, e := o.read(checkout, name, name, MaxDependencyBytes, false)
		if e != nil {
			return nil, e
		}
		o.value.Manifests = append(o.value.Manifests, f)
	}
	if err = o.Check(); err != nil {
		return nil, err
	}
	return o, nil
}
func (o *InputObservation) Check() error {
	if o == nil || o.closed || o.checkout == nil || o.value.Validate() != nil {
		return ErrBoundary
	}
	current, err := o.checkout.Stat()
	if err != nil || !SamePolicyFile(o.checkoutInfo, current) {
		return errors.Join(ErrBoundary, err)
	}
	fd, e := syscall.Openat(int(o.checkout.Fd()), ".bench", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if o.config == nil {
		if !errors.Is(e, syscall.ENOENT) {
			if e == nil {
				_ = syscall.Close(fd)
			}
			return ErrBoundary
		}
	} else {
		if e != nil {
			return errors.Join(ErrBoundary, e)
		}
		f := os.NewFile(uintptr(fd), "current config directory")
		info, e := f.Stat()
		_ = f.Close()
		retained, re := o.config.Stat()
		if e != nil || re != nil || !SamePolicyFile(o.configInfo, info) || !SamePolicyFile(o.configInfo, retained) {
			return errors.Join(ErrBoundary, e, re)
		}
	}
	for _, h := range o.files {
		f, e := openInput(h.parent, h.name)
		if h.file == nil {
			if !errors.Is(e, syscall.ENOENT) {
				if f != nil {
					_ = f.Close()
				}
				return ErrBoundary
			}
			continue
		}
		if e != nil {
			return errors.Join(ErrBoundary, e)
		}
		current, e := f.Stat()
		_ = f.Close()
		held, he := h.file.Stat()
		if e != nil || he != nil || !SamePolicyFile(h.info, current) || !SamePolicyFile(h.info, held) {
			return errors.Join(ErrBoundary, e, he)
		}
	}
	current, err = o.checkout.Stat()
	if err != nil || !SamePolicyFile(o.checkoutInfo, current) {
		return errors.Join(ErrBoundary, err)
	}
	if o.config != nil {
		info, e := o.config.Stat()
		if e != nil || !SamePolicyFile(o.configInfo, info) {
			return errors.Join(ErrBoundary, e)
		}
	}
	return nil
}

// ObserveInputs uses only an already-held registered checkout node. The caller
// retains Nodes and this observation through final policy/registration checks.
func (b *Nodes) ObserveInputs(p Policy, l Layout, member string) (*InputObservation, error) {
	if b == nil || b.Check(p, l) != nil {
		return nil, ErrBoundary
	}
	var expected Node
	for _, m := range l.Members {
		if m.Name == member {
			expected = m.Checkout
		}
	}
	if expected.Inode == 0 {
		return nil, ErrBoundary
	}
	for _, f := range b.files {
		if MatchesNode(f, expected) {
			return readInputs(f, member, p.PayloadUID, p.PayloadGID, l.Root.Device)
		}
	}
	return nil, ErrBoundary
}
