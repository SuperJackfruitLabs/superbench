package registeredinspection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"unicode/utf8"
)

const InputsVersion = "superbench.registered-inputs.v1"
const MaxConfigBytes = 64 << 10
const MaxDependencyBytes = 16 << 20

// One base64 config plus three fixed hash records fits this private bound.
const MaxInputs = 128 << 10

var memberName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var lowerHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var dependencyNames = [3]string{"package.json", "pnpm-lock.yaml", "package-lock.json"}

// InputFile records only a fixed preparation input. Dependency contents never
// cross this channel; config bytes remain private to the unprivileged parser.
type InputFile struct {
	Name    string
	Present bool
	Bytes   uint64
	SHA256  string
	Data    []byte `json:",omitempty"`
}
type Inputs struct {
	Version, Member string
	Config          InputFile
	Manifests       []InputFile
}

func validInputFile(f InputFile, name string, max uint64, data bool) bool {
	if f.Name != name || f.Bytes > max {
		return false
	}
	if !f.Present {
		return f.Bytes == 0 && f.SHA256 == "" && len(f.Data) == 0
	}
	if !lowerHash.MatchString(f.SHA256) || !data && len(f.Data) != 0 {
		return false
	}
	if data {
		sum := sha256.Sum256(f.Data)
		return uint64(len(f.Data)) == f.Bytes && hex.EncodeToString(sum[:]) == f.SHA256
	}
	return true
}
func (i Inputs) Validate() error {
	if i.Version != InputsVersion || !memberName.MatchString(i.Member) || !validInputFile(i.Config, ".bench/config.toml", MaxConfigBytes, true) || len(i.Manifests) != len(dependencyNames) {
		return ErrBoundary
	}
	for n, name := range dependencyNames {
		if !validInputFile(i.Manifests[n], name, MaxDependencyBytes, false) {
			return ErrBoundary
		}
	}
	return nil
}
func (i Inputs) Bytes() ([]byte, error) {
	if i.Validate() != nil {
		return nil, ErrBoundary
	}
	raw, err := json.Marshal(i)
	if err != nil || len(raw) > MaxInputs {
		return nil, ErrBoundary
	}
	return raw, nil
}
func ParseInputs(raw []byte) (Inputs, error) {
	var i Inputs
	if len(raw) > MaxInputs || !utf8.Valid(raw) || json.Unmarshal(raw, &i) != nil {
		return Inputs{}, ErrBoundary
	}
	canonical, err := i.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return Inputs{}, ErrBoundary
	}
	return i, nil
}

// PreparationDigest preserves the existing prepinput hash ordering. The caller
// must first parse the actual config successfully; its digest must match these
// preserved bytes. This computes identity only, never preparation success.
func (i Inputs) PreparationDigest(configDigest string) (string, error) {
	if i.Validate() != nil || !i.Config.Present && configDigest != "" || i.Config.Present && configDigest != i.Config.SHA256 {
		return "", ErrBoundary
	}
	h := sha256.New()
	if i.Config.Present {
		_, _ = h.Write([]byte(configDigest))
	}
	for _, f := range i.Manifests {
		_, _ = h.Write([]byte("\x00" + f.Name + "\x00"))
		if !f.Present {
			_, _ = h.Write([]byte("absent"))
			continue
		}
		raw, err := hex.DecodeString(f.SHA256)
		if err != nil {
			return "", ErrBoundary
		}
		_, _ = h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
