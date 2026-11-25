package activation

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

const remoteHead = `{"version":"superbench.activation.v1","fresh_payload":true,"cancellation":true`

func okCheck() error { return nil }

func TestRemoteAccessParses(t *testing.T) {
	s, err := Parse([]byte(remoteHead + `,"remote_access":true,"remote_fleet_address":"100.64.0.7:2222"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !s.RemoteAccess || s.RemoteFleetAddress != netip.MustParseAddrPort("100.64.0.7:2222") || !s.Any() {
		t.Fatalf("%+v", s)
	}
	if err := s.Check(okCheck, okCheck); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s.String(), " remote_access=true remote_fleet_address=100.64.0.7:2222") {
		t.Fatal(s.String())
	}
	only, err := Parse([]byte(`{"version":"superbench.activation.v1","remote_access":true}`))
	if err != nil || !only.Any() {
		t.Fatal(only, err)
	}
}

func TestRemoteFleetAddressRefusals(t *testing.T) {
	for _, value := range []string{`"0.0.0.0:2222"`, `"[::]:2222"`, `"[::ffff:0.0.0.0]:2222"`, `"host:2222"`, `"100.64.0.7:0"`,
		`"100.64.0.7"`, `2222`, `null`, `"[fe80::1%eth0]:22"`, `""`, `true`} {
		_, err := Parse([]byte(remoteHead + `,"remote_access":true,"remote_fleet_address":` + value + `}`))
		if !errors.Is(err, ErrActivation) {
			t.Errorf("%s: %v", value, err)
		}
	}
}

func TestRemoteAccessRequiresFreshPayload(t *testing.T) {
	s, err := Parse([]byte(`{"version":"superbench.activation.v1","remote_access":true,"remote_fleet_address":"100.64.0.7:2222"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(okCheck, okCheck); err == nil || !strings.Contains(err.Error(), "remote_access requires fresh_payload") {
		t.Fatal(err)
	}
}

func TestRemoteAccessRequiresAddress(t *testing.T) {
	s, err := Parse([]byte(remoteHead + `,"remote_access":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(okCheck, okCheck); err == nil || !strings.Contains(err.Error(), "remote_access requires remote_fleet_address") {
		t.Fatal(err)
	}
}

func TestAddressRequiresRemoteAccess(t *testing.T) {
	s, err := Parse([]byte(remoteHead + `,"remote_fleet_address":"100.64.0.7:2222"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(okCheck, okCheck); err == nil || !strings.Contains(err.Error(), "remote_fleet_address requires remote_access") {
		t.Fatal(err)
	}
}

func TestPublicListenerKeyIsUnknown(t *testing.T) {
	for _, key := range []string{"remote_access_public", "remote_public_address"} {
		_, err := Parse([]byte(remoteHead + `,"` + key + `":true}`))
		if !errors.Is(err, ErrActivation) || !strings.Contains(err.Error(), "unknown key "+key) {
			t.Errorf("%s: %v", key, err)
		}
	}
}

func TestDefaultsKeepRemoteOff(t *testing.T) {
	for _, raw := range []string{`{"version":"superbench.activation.v1"}`,
		`{"version":"superbench.activation.v1","remote_access":false}`} {
		s, err := Parse([]byte(raw))
		if err != nil || s.RemoteAccess || s.RemoteFleetAddress.IsValid() || s.Any() {
			t.Errorf("%s: %+v %v", raw, s, err)
		}
	}
	var zero Switches
	if zero.RemoteAccess || zero.RemoteFleetAddress.IsValid() {
		t.Fatal(zero)
	}
}
