package registrationobserve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines int
}

func (w *deadlineRecorder) SetWriteDeadline(time.Time) error {
	w.deadlines++
	return errors.New("deadline reached after admission")
}

func TestHTTPAdmissionRefusesBeforeDeadlineAuthorityOrState(t *testing.T) {
	for _, kind := range []string{"admit-error", "slots-full"} {
		t.Run(kind, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "absent-state")
			request := protocolv2.RegistrationObserveRequest{Version: protocolv2.Version, Operation: "registration_observe", Key: "card", BenchID: strings.Repeat("a", 32), Holder: "owner", LeaseToken: strings.Repeat("b", 32)}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			h := HTTPHandler{Context: context.Background(), Store: bench.Store{State: state}, Operators: map[uint32]bool{1001: true}, Slots: make(chan struct{}, 1), Admit: func(*http.Request) error {
				calls++
				if kind == "admit-error" {
					return errors.New("private admission error")
				}
				return nil
			}}
			if kind == "slots-full" {
				h.Slots <- struct{}{}
			}
			occupied := len(h.Slots)
			r := httptest.NewRequest("POST", "/v2/registrations/observe", strings.NewReader(string(raw)))
			r.Header.Set("Content-Type", "application/json")
			w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.Serve(w, r, 1001)
			response, err := protocolv2.ParseRegistrationObserveResponse(w.Body.Bytes(), w.Code, request)
			if err != nil || w.Code != 503 || response.Error == nil || response.Error.Code != "unavailable" || response.Result != nil {
				t.Fatalf("admission response: %d %s %v", w.Code, w.Body, err)
			}
			if calls != 1 || w.deadlines != 0 || len(h.Slots) != occupied {
				t.Fatalf("admission bypassed: calls=%d deadlines=%d slots=%d, want %d", calls, w.deadlines, len(h.Slots), occupied)
			}
			if _, err := os.Lstat(state); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("admission touched state: %v", err)
			}
			if strings.Contains(w.Body.String(), request.LeaseToken) || strings.Contains(w.Body.String(), "private admission error") {
				t.Fatal("admission leaked private data")
			}
		})
	}
}

func TestHTTPOwnershipErrorMapping(t *testing.T) {
	request := protocolv2.RegistrationObserveRequest{Version: protocolv2.Version, Operation: "registration_observe", Key: "card", BenchID: strings.Repeat("a", 32), Holder: "owner", LeaseToken: strings.Repeat("b", 32)}
	for _, c := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"wrong-token", errors.Join(ErrObservation, bench.ErrLease), 409, "conflict"},
		{"unknown-key", errors.Join(ErrObservation, bench.ErrNotFound), 404, "not_found"},
		{"other", errors.New("private observation error"), 503, "unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Supply only the error to the exact production error-reply handler;
			// no installed authority or observation is fabricated.
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				replyObservationError(w, request, c.err)
			})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v2/registrations/observe", nil))
			response, err := protocolv2.ParseRegistrationObserveResponse(w.Body.Bytes(), w.Code, request)
			if err != nil || w.Code != c.status || response.Result != nil || response.Error == nil || response.Error.Code != c.code {
				t.Fatalf("ownership response: %d %s %v", w.Code, w.Body, err)
			}
			if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), request.LeaseToken) || strings.Contains(w.Body.String(), "private observation error") {
				t.Fatal("ownership response disclosed private details or allowed caching")
			}
		})
	}
}
