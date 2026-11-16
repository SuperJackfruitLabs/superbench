package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

func TestRegistrationObservationUsesActualUnixPeerNotClaimedHeaders(t *testing.T) {
	for _, operator := range []bool{false, true} {
		t.Run(map[bool]string{false: "reader", true: "operator-without-installed-authority"}[operator], func(t *testing.T) {
			root, err := os.MkdirTemp("", "sbo-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			path := filepath.Join(root, "s")
			state := filepath.Join(root, "missing-state")
			l, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			uid := uint32(os.Getuid())
			s := &Server{ctx: context.Background(), proposedRegistrationObservation: true, config: Config{Store: bench.Store{State: state}, Operators: map[uint32]bool{uid: operator}, Readers: map[uint32]bool{uid: !operator}}, slots: make(chan struct{}, 1)}
			listener := peerListener{Listener: l, slots: make(chan struct{}, 8), normal: make(chan struct{}, 2), reads: make(chan struct{}, 2)}
			server := &http.Server{Handler: s, ConnContext: connContext, ReadHeaderTimeout: time.Second}
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close(); <-done })
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			}}
			defer transport.CloseIdleConnections()
			body := `{"version":"superbench.v2","operation":"registration_observe","key":"card","bench_id":"` + strings.Repeat("a", 32) + `","holder":"fixture-owner","lease_token":"` + strings.Repeat("b", 32) + `"}`
			r, err := http.NewRequest("POST", "http://private/v2/registrations/observe", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Operator-UID", "0")
			r.Header.Set("Authorization", "Bearer forged")
			response, err := (&http.Client{Transport: transport, Timeout: 2 * time.Second}).Do(r)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
			_ = response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := 403
			if operator {
				want = 503
			}
			if response.StatusCode != want || strings.Contains(string(raw), strings.Repeat("b", 32)) || strings.Contains(string(raw), state) {
				t.Fatalf("kernel peer bypass or private output: %d %s", response.StatusCode, raw)
			}
			if _, err = os.Lstat(state); !os.IsNotExist(err) {
				t.Fatalf("route changed state: %v", err)
			}
		})
	}
}
