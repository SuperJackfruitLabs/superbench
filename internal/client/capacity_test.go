package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func TestCapacityResponseIsActionableNonzeroAndNeverRetriesCreate(t *testing.T) {
	request, _, _ := responseFixture(t, "create-ready")
	const message = "logical reservation capacity exhausted; needed_bytes=6500 available_bytes=6000 needed_inodes=400 available_inodes=900"
	for _, code := range []string{"capacity_exhausted", "capacity_arbitrary"} {
		t.Run(code, func(t *testing.T) {
			var calls atomic.Int32
			socket := unixService(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(422)
				_ = json.NewEncoder(w).Encode(protocolv2.Response{Version: protocolv2.Version, Operation: request.Operation, RequestID: request.RequestID, Error: &protocolv2.APIError{Code: code, Message: message}})
			})
			response, err := CallV2(context.Background(), socket, request)
			if calls.Load() != 1 {
				t.Fatal("refusal retried create", calls.Load())
			}
			if code == "capacity_arbitrary" {
				if !errors.Is(err, protocolv2.ErrResponse) {
					t.Fatal("unknown code generalized", err)
				}
				return
			}
			var failed *WorkspaceError
			if !errors.As(err, &failed) || failed.Code != code || failed.Message != message || failed.ExitCode() != 1 || response.Result != nil {
				t.Fatal("capacity quantities or nonzero refusal lost", response, err)
			}
		})
	}
}
