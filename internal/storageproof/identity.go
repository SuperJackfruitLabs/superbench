// Package storageproof owns bounded, canonical read-only storage evidence.
package storageproof

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
)

var Key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var volumePattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const MaxIdentity = 65536
const Version = "superbench.storage-identity.v1"

// Identity binds an existing physical data root. Managed identities also
// pin its dedicated filesystem, fixed policy, existing ledger and owner anchor.
// It is evidence to recheck, never permission to create or adopt a directory.
type Identity struct {
	Version, Root, BenchKey                                                string
	Device, Inode                                                          uint64
	Mount                                                                  string
	Managed                                                                bool
	FilesystemUUID, PolicySHA256, LedgerSHA256, AnchorSHA256, ConfigSHA256 string `json:",omitempty"`
	Assignment                                                             quotahelper.Assignment
}

func (s Identity) Validate() error {
	if s.Version != Version || !Key.MatchString(s.BenchKey) || !utf8.ValidString(s.Root) || !utf8.ValidString(s.Mount) || !filepath.IsAbs(s.Root) || filepath.Clean(s.Root) != s.Root || s.Root == "/" || s.Device == 0 || s.Inode == 0 || s.Mount == "" || len(s.Mount) > 8192 || s.Assignment.Path != filepath.Join(s.Root, "benches", s.BenchKey) {
		return errors.New("invalid storage identity")
	}
	if s.Managed {
		if !volumePattern.MatchString(s.FilesystemUUID) || s.Assignment.ProjectID < 10000 || s.Assignment.Bytes == 0 || s.Assignment.Inodes == 0 {
			return errors.New("invalid managed storage identity")
		}
		for _, digest := range []string{s.PolicySHA256, s.LedgerSHA256, s.AnchorSHA256, s.ConfigSHA256} {
			decoded, err := hex.DecodeString(digest)
			if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != digest {
				return errors.New("invalid storage digest")
			}
		}
	} else if s.FilesystemUUID != "" || s.PolicySHA256 != "" || s.LedgerSHA256 != "" || s.AnchorSHA256 != "" || s.ConfigSHA256 != "" || s.Assignment.ProjectID != 0 || s.Assignment.Bytes != 0 || s.Assignment.Inodes != 0 {
		return errors.New("unmanaged storage cannot claim managed evidence")
	}
	return nil
}

func (s Identity) Bytes() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(s)
	if len(raw) > MaxIdentity {
		return nil, errors.New("storage identity exceeds limit")
	}
	return raw, err
}

func ParseIdentity(raw []byte) (Identity, error) {
	var s Identity
	if len(raw) > MaxIdentity {
		return s, errors.New("storage identity exceeds limit")
	}
	if err := StrictJSON(raw, &s); err != nil {
		return Identity{}, err
	}
	canonical, err := s.Bytes()
	if err != nil {
		return Identity{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return Identity{}, errors.New("storage identity is not canonical")
	}
	return s, nil
}

func StrictJSON(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing storage JSON")
	}
	return nil
}

func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
