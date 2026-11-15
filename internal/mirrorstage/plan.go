package mirrorstage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

const PlanVersion = "superbench.mirror-stage-collection.v1"
const MaxPlan = reap.MaxManifest

type Plan struct {
	Version    string
	Intent     Intent
	Root       Root
	Published  *Identity `json:",omitempty"`
	Tree       reap.Tree
	ObservedAt time.Time
	Digest     string
}
type Result struct {
	ID, Digest string
	Completed  bool
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (p Plan) digest() string { p.Digest = ""; return digest(p) }
func validHash(s string, n int) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == n && hex.EncodeToString(b) == s
}
func utc(t time.Time) bool { _, off := t.Zone(); return !t.IsZero() && off == 0 }
func (p Plan) Bytes() ([]byte, error) {
	i := p.Intent
	if p.Version != PlanVersion || !validID(i.ID) || p.Root.ID != i.ID || i.Version != Version || i.ControlRoot == "" || !utc(i.CreatedAt) || !utc(p.ObservedAt) || p.Digest != p.digest() {
		return nil, errors.New("invalid mirror stage collection approval")
	}
	for _, path := range []string{i.State, i.Parent, i.ControlRoot} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || !utf8.ValidString(path) {
			return nil, errors.New("canonical collection authority paths required")
		}
	}
	name := filepath.Base(i.Mirror)
	if i.Stage != filepath.Join(i.Parent, ".clone-"+i.ID) || i.Mirror != filepath.Join(i.Parent, name) || len(name) != 68 || !strings.HasSuffix(name, ".git") || !validHash(strings.TrimSuffix(name, ".git"), 32) {
		return nil, errors.New("collection stage paths differ from intent")
	}
	root := p.Tree.Root
	if root.Path != "." || root.Device != p.Root.Identity.Device || root.Inode != p.Root.Identity.Inode || root.Mode != p.Root.Identity.Mode || !os.FileMode(root.Mode).IsDir() || p.Root.Identity.Inode == 0 || i.ParentIdentity.Inode == 0 || os.FileMode(i.ParentIdentity.Mode).Type() != os.ModeDir || root.Device != i.ParentIdentity.Device || os.FileMode(root.Mode).Perm() != 0700 || len(p.Tree.Entries) > reap.MaxEntries {
		return nil, errors.New("collection root differs from durable stage")
	}
	entries := map[string]reap.Entry{".": root}
	previous := ""
	for index, entry := range append([]reap.Entry{root}, p.Tree.Entries...) {
		mode := os.FileMode(entry.Mode)
		if entry.Recoverable || entry.Device != root.Device || entry.Inode == 0 || !validHash(entry.SHA256, 32) || !(mode.IsRegular() || mode.IsDir() || mode.Type() == os.ModeSymlink) {
			return nil, errors.New("invalid collection entry")
		}
		if mode.IsDir() {
			if entry.GitSHA1 != "" || entry.GitSHA256 != "" || entry.SHA256 != digestEmpty {
				return nil, errors.New("invalid collection directory")
			}
		} else if !validHash(entry.GitSHA1, 20) || !validHash(entry.GitSHA256, 32) {
			return nil, errors.New("invalid collection object hashes")
		}
		if index == 0 {
			continue
		}
		if entry.Path == "." {
			return nil, errors.New("duplicate collection root")
		}
		if entry.Path <= previous || !utf8.ValidString(entry.Path) || filepath.IsAbs(entry.Path) || filepath.Clean(entry.Path) != entry.Path || entry.Path == ".." || strings.HasPrefix(entry.Path, ".."+string(filepath.Separator)) {
			return nil, errors.New("invalid collection entry path")
		}
		parent, ok := entries[filepath.Dir(entry.Path)]
		if !ok || !os.FileMode(parent.Mode).IsDir() {
			return nil, errors.New("collection entry parent missing")
		}
		entries[entry.Path] = entry
		previous = entry.Path
	}
	if p.Published != nil && (p.Published.Inode == 0 || p.Published.Device != root.Device || !os.FileMode(p.Published.Mode).IsDir() || len(p.Tree.Entries) != 0) {
		return nil, errors.New("published-stage cleanup requires an empty stage and pinned mirror")
	}
	raw, e := json.Marshal(p)
	if len(raw) > MaxPlan {
		return nil, errors.New("collection approval exceeds bound")
	}
	return raw, e
}

const digestEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func ParsePlan(raw []byte) (*Plan, error) {
	if len(raw) > MaxPlan {
		return nil, errors.New("collection approval exceeds bound")
	}
	var p Plan
	if e := decode(raw, &p); e != nil {
		return nil, e
	}
	canonical, e := p.Bytes()
	if e != nil {
		return nil, e
	}
	if !compactEqual(raw, canonical) {
		return nil, errors.New("noncanonical collection approval")
	}
	return &p, nil
}
func compactEqual(raw, canonical []byte) bool {
	var b bytes.Buffer
	return json.Compact(&b, raw) == nil && bytes.Equal(b.Bytes(), canonical)
}
func decode(raw []byte, v any) error {
	if e := reap.CheckJSON(raw); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
