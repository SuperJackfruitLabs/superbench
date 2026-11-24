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
	"net/netip"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	// FixedPath is the only activation document benchd reads.
	FixedPath = "/etc/superbench/activation.json"
	Version   = "superbench.activation.v1"
	MaxSize   = 4 << 10
	// DefaultReclaimIdleHours is the idle grace of a Fresh bench when
	// fresh_payload is on and fresh_reclaim_idle_hours is omitted.
	DefaultReclaimIdleHours = 24
	// MaxReclaimIdleHours bounds the grace at one year; 0 disables the sweep.
	MaxReclaimIdleHours = 8760
)

var ErrActivation = errors.New("invalid activation file")

// Switches are all false unless the installed file sets them explicitly.
type Switches struct {
	FreshPayload            bool // bench.Store Fresh payload layout
	FiniteIO                bool // finite-I/O profile on v2 run and receipt lookup
	ReceiptLookup           bool // POST /v2/receipts/lookup
	Cancellation            bool // v2 cancel requests and the operation reconciler
	RegistrationObservation bool // POST /v2/registrations/observe
	// ReclaimIdleHours is fresh_reclaim_idle_hours, set only when the file names
	// it (ReclaimIdleSet). It is a setting of fresh_payload, not a path of its own.
	ReclaimIdleHours uint32
	ReclaimIdleSet   bool
	// RemoteAccess starts the fleet-facing remote bench listener; it needs
	// fresh_payload and RemoteFleetAddress.
	RemoteAccess bool
	// RemoteFleetAddress is remote_fleet_address, the one fleet listener
	// address. Zero (invalid) unless the file names it.
	RemoteFleetAddress netip.AddrPort
}

// Any reports whether at least one default-off path is enabled. The idle
// grace is not a path: Check refuses it without fresh_payload.
func (s Switches) Any() bool {
	return s.FreshPayload || s.FiniteIO || s.ReceiptLookup || s.Cancellation || s.RegistrationObservation || s.RemoteAccess
}

// ReclaimIdle is the effective idle grace of a released Fresh bench before the
// daemon reclaims it: 0 (off) unless fresh_payload is on, then the file's
// fresh_reclaim_idle_hours, or 24 hours when it is omitted. 0 hours disables it.
func (s Switches) ReclaimIdle() time.Duration {
	if !s.FreshPayload {
		return 0
	}
	hours := uint32(DefaultReclaimIdleHours)
	if s.ReclaimIdleSet {
		hours = s.ReclaimIdleHours
	}
	return time.Duration(hours) * time.Hour
}

func (s Switches) String() string {
	return fmt.Sprintf("fresh_payload=%t finite_io=%t receipt_lookup=%t cancellation=%t registration_observation=%t fresh_reclaim_idle=%s remote_access=%t remote_fleet_address=%s",
		s.FreshPayload, s.FiniteIO, s.ReceiptLookup, s.Cancellation, s.RegistrationObservation, s.ReclaimIdle(),
		s.RemoteAccess, s.RemoteFleetAddress)
}

// validIdleHours is the accepted range of fresh_reclaim_idle_hours: 0 turns the
// sweep off, otherwise at least one hour and at most a year.
func validIdleHours(hours uint32) bool { return hours <= MaxReclaimIdleHours }

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
	case "remote_access":
		return &s.RemoteAccess
	}
	return nil
}

// Check refuses a combination that would strand durable state, before benchd
// opens its socket (the dependency table in docs/pilot-installation.md):
//   - fresh_payload needs cancellation: only the cancellation path claims the
//     operation scope Fresh materialization requires. Without it every v2 create
//     or recover would leave a durable materializing record behind.
//   - fresh_payload needs the installed payload policy and service identity,
//     which identity resolves, for the same reason.
//   - fresh_payload needs the indexed quota operation journal, which storage
//     checks: the payload helper's bootstrap refuses any other storage policy,
//     so every Fresh create would strand a materializing workspace.
//   - remote_access needs fresh_payload and remote_fleet_address, and the
//     address is meaningless without remote_access.
//   - finite_io and registration_observation act only on Fresh payload trees,
//     so they need fresh_payload. So does fresh_reclaim_idle_hours, which only
//     Fresh benches use, and it must be 0 or 1 to 8760 hours.
//
// receipt_lookup and cancellation have no prerequisite.
func (s Switches) Check(identity, storage func() error) error {
	switch {
	case s.RemoteAccess && !s.FreshPayload:
		return refuse("remote_access requires fresh_payload")
	case s.RemoteAccess && !s.RemoteFleetAddress.IsValid():
		return refuse("remote_access requires remote_fleet_address")
	case !s.RemoteAccess && s.RemoteFleetAddress.IsValid():
		return refuse("remote_fleet_address requires remote_access")
	case s.FiniteIO && !s.FreshPayload:
		return refuse("finite_io requires fresh_payload")
	case s.RegistrationObservation && !s.FreshPayload:
		return refuse("registration_observation requires fresh_payload")
	case s.ReclaimIdleSet && !s.FreshPayload:
		return refuse("fresh_reclaim_idle_hours requires fresh_payload")
	case !validIdleHours(s.ReclaimIdleHours):
		return refuse("fresh_reclaim_idle_hours must be 0 (off) or 1 to 8760")
	case s.FreshPayload && !s.Cancellation:
		return refuse("fresh_payload requires cancellation")
	case !s.FreshPayload:
		return nil
	case identity == nil:
		return refuse("fresh_payload requires the installed service identity")
	case storage == nil:
		return refuse("fresh_payload requires the indexed quota operation journal")
	}
	if err := identity(); err != nil {
		return refuse("fresh_payload requires the installed payload policy and service identity: " + err.Error())
	}
	if err := storage(); err != nil {
		return refuse("fresh_payload requires the indexed quota operation journal: " + err.Error())
	}
	return nil
}

func refuse(why string) error { return fmt.Errorf("%w: %s", ErrActivation, why) }

// Parse accepts one JSON object with the exact version and any subset of the
// five switches, each a JSON boolean, and fresh_reclaim_idle_hours, a JSON
// integer from 0 to 8760 written without a fraction or exponent. An omitted
// switch is false. Unknown, duplicate or mistyped keys, null, nesting and
// trailing data are refused.
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
		if key == "fresh_reclaim_idle_hours" {
			hours, ok := idleHours(value)
			if !ok {
				return Switches{}, refuse("fresh_reclaim_idle_hours must be a whole number of hours, 0 (off) or 1 to 8760")
			}
			s.ReclaimIdleHours, s.ReclaimIdleSet = hours, true
			continue
		}
		if key == "remote_fleet_address" {
			addr, ok := fleetAddress(value)
			if !ok {
				return Switches{}, refuse("remote_fleet_address must be an ip:port string with a nonzero port, no zone and a specific address")
			}
			s.RemoteFleetAddress = addr
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

// idleHours accepts only a JSON number that is a plain decimal integer in
// range: ParseUint refuses a sign, fraction or exponent, and the decoder a
// leading zero.
func idleHours(value json.Token) (uint32, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	hours, err := strconv.ParseUint(string(n), 10, 32)
	if err != nil || !validIdleHours(uint32(hours)) {
		return 0, false
	}
	return uint32(hours), true
}

// fleetAddress accepts only a JSON string that is a literal ip:port: not a host
// name, no zone, port above 0 and not the unspecified address.
func fleetAddress(value json.Token) (netip.AddrPort, bool) {
	text, ok := value.(string)
	if !ok {
		return netip.AddrPort{}, false
	}
	addr, err := netip.ParseAddrPort(text)
	if err != nil || addr.Port() == 0 || addr.Addr().IsUnspecified() || addr.Addr().Zone() != "" {
		return netip.AddrPort{}, false
	}
	return addr, true
}
