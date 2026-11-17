// Package activation reads the root-owned switches that turn on benchd's
// default-off v2 paths. Only the fixed installed file can set them: no request,
// environment variable or command-line flag reaches this package.
package activation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	// FixedPath is the only activation document benchd reads.
	FixedPath = "/etc/superbench/activation.json"
	Version   = "superbench.activation.v1"
	MaxSize   = 4 << 10
)

var ErrActivation = errors.New("invalid activation file")

// Switches are all false unless the installed file sets them explicitly.
type Switches struct {
	FreshPayload            bool // bench.Store Fresh payload layout
	FiniteIO                bool // finite-I/O profile on v2 run and receipt lookup
	ReceiptLookup           bool // POST /v2/receipts/lookup
	Cancellation            bool // v2 cancel requests and the operation reconciler
	RegistrationObservation bool // POST /v2/registrations/observe
}

// Any reports whether at least one default-off path is enabled.
func (s Switches) Any() bool { return s != Switches{} }

func (s Switches) String() string {
	return fmt.Sprintf("fresh_payload=%t finite_io=%t receipt_lookup=%t cancellation=%t registration_observation=%t",
		s.FreshPayload, s.FiniteIO, s.ReceiptLookup, s.Cancellation, s.RegistrationObservation)
}

func (s *Switches) field(name string) *bool {
	switch name {
	case "fresh_payload":
		return &s.FreshPayload
	case "finite_io":
		return &s.FiniteIO
	case "receipt_lookup":
		return &s.ReceiptLookup
	case "cancellation":
		return &s.Cancellation
	case "registration_observation":
		return &s.RegistrationObservation
	}
	return nil
}

func refuse(why string) error { return fmt.Errorf("%w: %s", ErrActivation, why) }

// Parse accepts one JSON object with the exact version and any subset of the
// five switches, each a JSON boolean. An omitted switch is false. Unknown,
// duplicate or non-boolean keys, null, nesting and trailing data are refused.
func Parse(raw []byte) (Switches, error) {
	var s Switches
	if len(raw) == 0 || len(raw) > MaxSize || !utf8.Valid(raw) {
		return s, refuse("size or encoding")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return Switches{}, refuse("not a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return Switches{}, refuse("malformed key")
		}
		if seen[key] {
			return Switches{}, refuse("duplicate key " + key)
		}
		seen[key] = true
		value, err := dec.Token()
		if err != nil {
			return Switches{}, refuse("malformed value for " + key)
		}
		if key == "version" {
			if value != Version {
				return Switches{}, refuse("version must be " + Version)
			}
			continue
		}
		target := s.field(key)
		if target == nil {
			return Switches{}, refuse("unknown key " + key)
		}
		flag, ok := value.(bool)
		if !ok {
			return Switches{}, refuse(key + " must be true or false")
		}
		*target = flag
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		return Switches{}, refuse("unterminated object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return Switches{}, refuse("trailing data")
	}
	if !seen["version"] {
		return Switches{}, refuse("version is required")
	}
	return s, nil
}
