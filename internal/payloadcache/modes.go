// Package payloadcache describes fixed registered-payload cache selections.
// A mode or derived environment is not physical cache registration, quota or
// launch authority. The fixed helper must independently verify that custody.
package payloadcache

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
)

const Version = "superbench.payload-caches.v1"
const MaxModes = 1024

var ErrModes = errors.New("invalid registered payload cache modes")
var ErrUnavailable = errors.New("registered payload cache custody unavailable")

// True means the existing shared cache setting; false means checkout-local.
// All five booleans are required on the wire, including false values.
type Modes struct {
	Version    string `json:"version"`
	Pnpm       bool   `json:"pnpm"`
	Npm        bool   `json:"npm"`
	Bun        bool   `json:"bun"`
	Playwright bool   `json:"playwright"`
	Go         bool   `json:"go"`
}

func shared(p *bool) bool { return p == nil || *p }
func FromSettings(s cachepolicy.Settings) Modes {
	return Modes{Version: Version, Pnpm: shared(s.Pnpm), Npm: shared(s.Npm), Bun: shared(s.Bun), Playwright: shared(s.Playwright), Go: shared(s.Go)}
}
func (m Modes) Validate() error {
	if m.Version != Version {
		return ErrModes
	}
	return nil
}
func (m Modes) Bytes() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > MaxModes {
		return nil, ErrModes
	}
	return raw, nil
}
func (m *Modes) UnmarshalJSON(raw []byte) error {
	if m == nil || len(raw) > MaxModes || !utf8.Valid(raw) {
		return ErrModes
	}
	type plain Modes
	var next plain
	if json.Unmarshal(raw, &next) != nil {
		return ErrModes
	}
	value := Modes(next)
	canonical, err := value.Bytes()
	var compact bytes.Buffer
	if err != nil || json.Compact(&compact, raw) != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return ErrModes
	}
	*m = value
	return nil
}
