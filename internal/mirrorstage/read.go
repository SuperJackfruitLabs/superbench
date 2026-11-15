package mirrorstage

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
)

const MaxRecordBytes = 16 << 10

func validID(id string) bool {
	raw, e := hex.DecodeString(id)
	return e == nil && len(raw) == 16 && hex.EncodeToString(raw) == id
}

func readRecord(dir, name string, v any) error {
	anchor, e := bind(dir)
	if e != nil {
		return e
	}
	defer anchor.close()
	return readBound(anchor, name, v)
}
func readBound(anchor *boundDir, name string, v any) error {
	return readLimit(anchor, name, v, MaxRecordBytes)
}
func readLimit(anchor *boundDir, name string, v any, limit int) error {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("fixed journal record name required")
	}
	if e := anchor.check(); e != nil {
		return e
	}
	f, e := anchor.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > int64(limit) {
		return errors.New("bounded private mirror stage record required")
	}
	raw, e := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if e != nil {
		return e
	}
	if len(raw) > limit {
		return errors.New("mirror stage record exceeds bound")
	}
	if e = reap.CheckJSON(raw); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	canonical, e := json.Marshal(v)
	if e != nil {
		return e
	}
	var compact bytes.Buffer
	if e = json.Compact(&compact, raw); e != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return errors.New("mirror stage record is not canonical")
	}
	named, e := anchor.root.Lstat(name)
	if e != nil {
		return e
	}
	after, e := f.Stat()
	if e != nil {
		return e
	}
	if !os.SameFile(info, named) || !os.SameFile(info, after) || after.Size() != info.Size() || after.Mode() != info.Mode() || !after.ModTime().Equal(info.ModTime()) {
		return errors.New("mirror stage record changed while reading")
	}
	return anchor.check()
}

// Load reads both immutable records. A reservation without a root record is
// deliberately not sufficient authority over a directory, even at its exact
// reserved name. This method never creates/adopts/deletes staging data.
func Load(state, parent, id string) (*Intent, *Root, error) {
	if e := statepath.Validate(state, parent); e != nil {
		return nil, nil, e
	}
	if !validID(id) {
		return nil, nil, errors.New("valid mirror stage id required")
	}
	state, e := physical(state)
	if e != nil {
		return nil, nil, e
	}
	parent, e = physical(parent)
	if e != nil {
		return nil, nil, e
	}
	stateDir, e := bind(state)
	if e != nil {
		return nil, nil, e
	}
	defer stateDir.close()
	parentDir, e := bind(parent)
	if e != nil {
		return nil, nil, e
	}
	defer parentDir.close()
	dir, e := openJournal(stateDir, false)
	if e != nil {
		return nil, nil, e
	}
	defer dir.close()
	var p Intent
	if e = readBound(dir, id+".json", &p); e != nil {
		return nil, nil, e
	}
	var root Root
	if e = readBound(dir, id+"-root.json", &root); e != nil {
		return nil, nil, e
	}
	_, offset := p.CreatedAt.Zone()
	if p.ControlRoot != "" && (!filepath.IsAbs(p.ControlRoot) || filepath.Clean(p.ControlRoot) != p.ControlRoot) {
		return nil, nil, errors.New("noncanonical managed Git control provenance")
	}
	if p.Version != Version || p.ID != id || p.State != state || p.Parent != parent || p.Stage != filepath.Join(parent, ".clone-"+id) || p.Mirror != filepath.Join(parent, filepath.Base(p.Mirror)) || len(filepath.Base(p.Mirror)) != 68 || filepath.Ext(p.Mirror) != ".git" || p.CreatedAt.IsZero() || offset != 0 || root.ID != id {
		return nil, nil, errors.New("mirror stage identity differs from its reservation")
	}
	name := filepath.Base(p.Mirror)
	hash, e := hex.DecodeString(name[:64])
	if e != nil || hex.EncodeToString(hash) != name[:64] {
		return nil, nil, errors.New("mirror stage publication target is not hashed")
	}
	current := parentDir.id
	if current != p.ParentIdentity || root.Identity.Device != current.Device || root.Identity.Inode == 0 || !os.FileMode(root.Identity.Mode).IsDir() || os.FileMode(root.Identity.Mode).Perm() != 0700 {
		return nil, nil, errors.New("mirror stage parent or recorded root changed")
	}
	if e = checkDirs(stateDir, dir, parentDir); e != nil {
		return nil, nil, e
	}
	return &p, &root, nil
}

// CheckRoot is a live observation, not evidence that the clone writer stopped.
// The host lock and recovered Git control scopes remain separate prerequisites.
func CheckRoot(p Intent, root Root) error {
	if !validID(p.ID) || root.ID != p.ID || p.Stage != filepath.Join(p.Parent, ".clone-"+p.ID) {
		return errors.New("mirror stage root differs from reservation")
	}
	parent, e := bind(p.Parent)
	if e != nil {
		return e
	}
	defer parent.close()
	if parent.id != p.ParentIdentity {
		return errors.New("mirror stage parent was replaced")
	}
	leaf, e := parent.child(filepath.Base(p.Stage))
	if e != nil {
		return e
	}
	defer leaf.close()
	if leaf.id != root.Identity {
		return errors.New("mirror stage directory was replaced")
	}
	return checkDirs(parent, leaf)
}
