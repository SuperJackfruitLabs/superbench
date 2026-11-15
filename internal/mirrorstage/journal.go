// Package mirrorstage records ownership of unpublished mirror staging directories.
// Callers must serialize operations with the host store lock and reconcile Git
// control writers before inspection or collection. Age never establishes fencing.
package mirrorstage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
)

const Version = "superbench.mirror-stage.v1"
const MaxJournalEntries = 2000

type Identity struct {
	Device, Inode uint64
	Mode          uint32
}

type Intent struct {
	Version, ID, State, Parent, Mirror, Stage string
	ControlRoot                               string
	ParentIdentity                            Identity
	CreatedAt                                 time.Time
}

// Root is separate immutable evidence: a crash between mkdir and this record
// leaves an unfamiliar directory that ordinary collection must preserve.
type Root struct {
	ID       string
	Identity Identity
}

func identity(path string) (Identity, error) {
	s, e := os.Lstat(path)
	if e != nil {
		return Identity{}, e
	}
	return fileIdentity(s)
}

func fileIdentity(s os.FileInfo) (Identity, error) {
	if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
		return Identity{}, errors.New("physical staging directory required")
	}
	v := s.Sys().(*syscall.Stat_t)
	return Identity{uint64(v.Dev), v.Ino, uint32(s.Mode())}, nil
}

func physical(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("canonical absolute staging path required")
	}
	if _, e := identity(path); e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(path)
}

func journal(state string, create bool) (string, error) {
	state, e := physical(state)
	if e != nil {
		return "", e
	}
	bound, e := bind(state)
	if e != nil {
		return "", e
	}
	defer bound.close()
	dir, e := openJournal(bound, create)
	if e != nil {
		return "", e
	}
	defer dir.close()
	return dir.path, nil
}

// save is append-only. An incomplete or conflicting record is preserved and
// blocks reuse; no retry overwrites an unknown writer's bytes.
func save(dir, name string, value any) error {
	bound, e := bind(dir)
	if e != nil {
		return e
	}
	defer bound.close()
	return saveBound(bound, name, value)
}
func saveBound(dir *boundDir, name string, value any) error {
	return saveLimit(dir, name, value, MaxRecordBytes)
}
func saveLimit(dir *boundDir, name string, value any, limit int) error {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("fixed journal record name required")
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if len(raw) > limit {
		return errors.New("mirror stage record exceeds bound")
	}
	if e = dir.check(); e != nil {
		return e
	}
	f, e := dir.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if e = errors.Join(writeErr, closeErr); e != nil {
		return e
	}
	return dir.sync()
}

// Create durably reserves the exact private stage before mkdir and records its
// actual identity before a caller can start Git. Failed clones retain both files
// and records for an explicit review. Published mirrors are never candidates.
func Create(state, parent, mirror string) (*Intent, error) {
	return create(state, parent, mirror, "")
}

// CreateManaged pins the configured Git-control root before any clone effect.
// This records provenance only; reconciliation must still prove writers fenced.
func CreateManaged(state, parent, mirror, controlRoot string) (*Intent, error) {
	if !filepath.IsAbs(controlRoot) || filepath.Clean(controlRoot) != controlRoot {
		return nil, errors.New("canonical managed Git control root required")
	}
	return create(state, parent, mirror, controlRoot)
}

func create(state, parent, mirror, controlRoot string) (*Intent, error) {
	if e := statepath.Validate(state, parent); e != nil {
		return nil, e
	}
	state, e := physical(state)
	if e != nil {
		return nil, e
	}
	if filepath.Dir(mirror) != parent || filepath.Clean(mirror) != mirror || filepath.Ext(mirror) != ".git" {
		return nil, errors.New("mirror must be a fixed leaf of staging parent")
	}
	parent, e = physical(parent)
	if e != nil {
		return nil, e
	}
	mirror = filepath.Join(parent, filepath.Base(mirror))
	base := filepath.Base(mirror)
	if len(base) != 68 {
		return nil, errors.New("hashed mirror name required")
	}
	hash, e := hex.DecodeString(base[:64])
	if e != nil || hex.EncodeToString(hash) != base[:64] {
		return nil, errors.New("hashed mirror name required")
	}

	stateDir, e := bind(state)
	if e != nil {
		return nil, e
	}
	defer stateDir.close()
	parentDir, e := bind(parent)
	if e != nil {
		return nil, e
	}
	defer parentDir.close()
	dir, e := openJournal(stateDir, true)
	if e != nil {
		return nil, e
	}
	defer dir.close()
	// Read relative to the same journal anchor used for subsequent writes.
	f, e := dir.root.Open(".")
	if e != nil {
		return nil, e
	}
	entries, readErr := f.ReadDir(MaxJournalEntries + 1)
	closeErr := f.Close()
	if readErr == io.EOF {
		readErr = nil
	}
	if e = errors.Join(readErr, closeErr); e != nil {
		return nil, e
	}
	// Unknown entries consume the same hard budget; a reservation adds two files.
	if len(entries)+2 > MaxJournalEntries {
		return nil, errors.New("mirror staging journal budget exhausted; review collection")
	}
	var random [16]byte
	if _, e = rand.Read(random[:]); e != nil {
		return nil, e
	}
	id := hex.EncodeToString(random[:])
	p := &Intent{Version: Version, ID: id, State: state, Parent: parent, Mirror: mirror, Stage: filepath.Join(parent, ".clone-"+id), CreatedAt: time.Now().UTC(), ParentIdentity: parentDir.id, ControlRoot: controlRoot}
	if e = createStage(p, stateDir, dir, parentDir); e != nil {
		return nil, e
	}
	return p, nil
}

func createStage(p *Intent, state, dir, parent *boundDir) error {
	if e := checkDirs(state, dir, parent); e != nil {
		return e
	}
	if e := saveBound(dir, p.ID+".json", p); e != nil {
		return e
	}
	if e := checkDirs(state, dir, parent); e != nil {
		return e
	}
	if e := parent.root.Mkdir(filepath.Base(p.Stage), 0700); e != nil {
		return e
	}
	if e := parent.sync(); e != nil {
		return e
	}
	root, e := parent.child(filepath.Base(p.Stage))
	if e != nil {
		return e
	}
	defer root.close()
	// Pin the opened leaf before its identity can become collection authority.
	if e = root.sync(); e != nil {
		return e
	}
	if e = checkDirs(state, dir, parent, root); e != nil {
		return e
	}
	if e = saveBound(dir, p.ID+"-root.json", Root{ID: p.ID, Identity: root.id}); e != nil {
		return e
	}
	return checkDirs(state, dir, parent, root)
}
func checkDirs(dirs ...*boundDir) error {
	for _, d := range dirs {
		if e := d.check(); e != nil {
			return e
		}
	}
	return nil
}
