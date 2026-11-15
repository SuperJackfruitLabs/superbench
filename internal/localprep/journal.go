// Package localprep records explicitly cooperative local preparation. Its records
// are not supervised execution, setup receipts, fencing or automatic handoff proof.
package localprep

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

const Version = "superbench.local-cooperative-preparation.v1"

var ErrPending = errors.New("cooperative preparation requires explicit stopped-writer acknowledgement")
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Record struct {
	Version, Mode, Key, Identity, OwnerSHA256, Attempt, InputSHA256, StartedAt string
	ExitCode                                                                   *int `json:",omitempty"`
	Observed                                                                   bool
	Truncated                                                                  bool
	Acknowledged                                                               bool
	Successful                                                                 bool
	UncertainOnce                                                              bool
	RanOnce                                                                    bool
	AcknowledgedDigest                                                         string `json:",omitempty"`
	PreviousOnceSHA256                                                         string `json:",omitempty"`
	Digest                                                                     string
}

func sum(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func identity(b *model.Bench) string {
	raw, _ := json.Marshal([]string{b.Key, b.Repo, b.Branch, b.Path, b.Mirror, b.CreatedFrom})
	return sum(raw)
}
func owner(b *model.Bench) string {
	if b.Lease == nil {
		return ""
	}
	return sum([]byte(b.Lease.Token))
}
func recordPath(state, key string) (string, error) {
	if !keyPattern.MatchString(key) {
		return "", errors.New("invalid cooperative bench key")
	}
	return filepath.Join(state, "local-preparation-"+key+".json"), nil
}
func digest(r Record) string { r.Digest = ""; raw, _ := json.Marshal(r); return sum(raw) }
func read(state, key string) (*Record, error) {
	p, e := recordPath(state, key)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
		return nil, errors.New("invalid cooperative journal file")
	}
	raw, e := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if e != nil {
		return nil, e
	}
	var r Record
	if json.Unmarshal(raw, &r) != nil {
		return nil, errors.New("invalid cooperative journal")
	}
	canonical, _ := json.Marshal(r)
	started, e := time.Parse(time.RFC3339Nano, r.StartedAt)
	if !bytes.Equal(raw, canonical) || r.Version != Version || r.Mode != "cooperative" || r.Key != key || !hex64.MatchString(r.Identity) || !hex64.MatchString(r.OwnerSHA256) || !hex64.MatchString(r.InputSHA256) || !hex32.MatchString(r.Attempt) || (r.AcknowledgedDigest != "" && !hex64.MatchString(r.AcknowledgedDigest)) || r.Acknowledged != (r.AcknowledgedDigest != "") || r.Digest != digest(r) || e != nil || started.IsZero() || started.Location() != time.UTC || r.Successful && (!r.Acknowledged || !r.Observed || r.ExitCode == nil || *r.ExitCode != 0) || r.PreviousOnceSHA256 != "" && !hex64.MatchString(r.PreviousOnceSHA256) {
		return nil, errors.New("invalid cooperative journal")
	}
	return &r, nil
}
func save(state string, r *Record) error {
	p, e := recordPath(state, r.Key)
	if e != nil {
		return e
	}
	r.Digest = digest(*r)
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	if r.Acknowledged {
		if e = preserveAcknowledged(state, r.Attempt, raw); e != nil {
			return e
		}
	}
	f, e := os.CreateTemp(state, ".local-preparation-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = os.Rename(name, p); e != nil {
		return e
	}
	dir, e := os.Open(state)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

// CheckHandoff runs under the authoritative state lock. Missing evidence keeps
// legacy behavior; corrupt or unresolved records refuse rather than imply stop.
func CheckHandoff(state string, b *model.Bench) error {
	r, e := read(state, b.Key)
	if e != nil {
		return e
	}
	if r == nil {
		return nil
	}
	if r.Identity != identity(b) {
		return errors.New("cooperative journal bench identity changed")
	}
	if !r.Acknowledged {
		return ErrPending
	}
	return nil
}

// Acknowledged attempts retain their exact metadata beside the original log.
// Replacing the active per-key pointer never erases an older observation.
func preserveAcknowledged(state, attempt string, raw []byte) error {
	path := filepath.Join(state, "local-preparation-"+attempt+".record")
	// Publish only a fully written, synced temporary inode. A controller crash
	// cannot leave a partially written final immutable observation.
	f, e := os.CreateTemp(state, ".local-observation-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	e = os.Link(tmp, path)

	if os.IsExist(e) {
		existing, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return e
		}
		defer existing.Close()
		info, e := existing.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
			return errors.New("invalid preserved cooperative observation")
		}
		stored, e := io.ReadAll(io.LimitReader(existing, 64<<10+1))
		if e != nil {
			return e
		}
		if !bytes.Equal(stored, raw) {
			return errors.New("preserved cooperative observation changed")
		}
		return nil
	}
	return e
}
