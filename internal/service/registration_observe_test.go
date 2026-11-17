package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func TestRegistrationObservationDefaultOffAndOperatorOnlyBeforeBody(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")
	s := &Server{ctx: context.Background(), config: Config{Store: bench.Store{State: root}, Operators: map[uint32]bool{v2Operator: true}, Readers: map[uint32]bool{v2Reader: true}}, slots: make(chan struct{}, 1)}
	r := protocolv2.RegistrationObserveRequest{Version: protocolv2.Version, Operation: "registration_observe", Key: "card", BenchID: strings.Repeat("a", 32), Holder: "fixture-owner", LeaseToken: strings.Repeat("b", 32)}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	status, body := v2Raw(t, s, v2Operator, "POST", "/v2/registrations/observe", "application/json", raw)
	if status != 404 || strings.Contains(string(body), r.LeaseToken) {
		t.Fatalf("defaultoff route exposed: %d %s", status, body)
	}
	s.proposedRegistrationObservation = true
	for _, uid := range []uint32{v2Reader, 999} {
		status, body = v2Raw(t, s, uid, "POST", "/v2/registrations/observe", "application/json", []byte(strings.Repeat("x", protocolv2.MaxBody+1)))
		if status != 403 || strings.Contains(string(body), r.LeaseToken) {
			t.Fatalf("nonoperator admitted before body: %d %s", status, body)
		}
	}
	request := httptest.NewRequest("POST", "/v2/registrations/observe", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Operator-UID", "101")
	request.Header.Set("Authorization", "Bearer forged")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatalf("header substituted for kernel peer: %d", w.Code)
	}
	if _, err = os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal created state: %v", err)
	}
}

func TestRegistrationObservationStrictBodyAndClosedUnavailable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "never-created")
	s := &Server{ctx: context.Background(), proposedRegistrationObservation: true, config: Config{Store: bench.Store{State: root}, Operators: map[uint32]bool{v2Operator: true}}, slots: make(chan struct{}, 1)}
	r := protocolv2.RegistrationObserveRequest{Version: protocolv2.Version, Operation: "registration_observe", Key: "card", BenchID: strings.Repeat("a", 32), Holder: "fixture-owner", LeaseToken: strings.Repeat("b", 32)}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, method, media string
		body                []byte
		status              int
	}{
		{"method", "GET", "application/json", raw, 405},
		{"media", "POST", "text/plain", raw, 415},
		{"media-parameters", "POST", "application/json; charset=utf-8", raw, 415},
		{"large", "POST", "application/json", []byte(strings.Repeat("x", protocolv2.MaxBody+1)), 413},
		{"duplicate", "POST", "application/json", []byte(strings.Replace(string(raw), `"key":"card"`, `"key":"card","key":"card"`, 1)), 400},
		{"extra", "POST", "application/json", []byte(strings.TrimSuffix(string(raw), "}") + `,"request_id":"not-an-operation"}`), 400},
		{"no-installed-authority-proof", "POST", "application/json", raw, 503},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, body := v2Raw(t, s, v2Operator, c.method, "/v2/registrations/observe", c.media, c.body)
			response, e := protocolv2.ParseRegistrationObserveResponse(body, status, r)
			if status != c.status || e != nil || response.Result != nil || response.Error == nil || len(body) > protocolv2.MaxBody || strings.Contains(string(body), r.LeaseToken) || strings.Contains(string(body), root) {
				t.Fatalf("unbounded or private refusal: %d %s %v", status, body, e)
			}
		})
	}
	if len(s.slots) != 0 {
		t.Fatal("observation leaked active slot")
	}
	if _, err = os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("route created state: %v", err)
	}
}

// Each switch reaches exactly its own path; receipt lookup alone still promotes
// normal requests out of the private listener's accept class.
func TestActivationSwitchesReachTheServer(t *testing.T) {
	for i := range 5 {
		var a activation.Switches
		*[]*bool{&a.FreshPayload, &a.FiniteIO, &a.ReceiptLookup, &a.Cancellation, &a.RegistrationObservation}[i] = true
		s := newServer(context.Background(), Config{}, a, nil)
		got, want := []bool{s.config.Store.ProposedFreshPayload, s.proposedFiniteIO, s.proposedReceiptLookup, s.proposedCancellation, s.proposedRegistrationObservation}, make([]bool, 5)
		c, r := &peerConn{slot: make(chan struct{}, 1), normal: make(chan struct{}, 1)}, httptest.NewRequest("GET", "/healthz", nil)
		c.slot <- struct{}{}
		if want[i] = true; !slices.Equal(got, want) || s.promoteNormal(r.WithContext(context.WithValue(r.Context(), connectionKey{}, c))) != nil || (len(c.normal) == 1) != (a.ReceiptLookup || a.Cancellation) {
			t.Errorf("%v reached %v, promoted %d", a, got, len(c.normal))
		}
	}
}
