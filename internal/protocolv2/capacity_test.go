package protocolv2

import (
	"errors"
	"testing"
)

func TestCapacityRefusalIsExplicit422Only(t *testing.T) {
	request := Request{Version: Version, Operation: "create", RequestID: "capacity-create"}
	raw := []byte(`{"version":"superbench.v2","operation":"create","request_id":"capacity-create","ok":false,"error":{"code":"capacity_exhausted","message":"logical reservation capacity exhausted; needed_bytes=6500 available_bytes=6000 needed_inodes=400 available_inodes=900"}}`)
	response, err := ParseResponse(raw, 422, request)
	if err != nil || response.Error == nil || response.Error.Code != "capacity_exhausted" {
		t.Fatal("known capacity refusal rejected", response, err)
	}
	for _, status := range []int{200, 400, 409, 503} {
		if _, err := ParseResponse(raw, status, request); !errors.Is(err, ErrResponse) {
			t.Fatal("capacity accepted wrong HTTP status", status, err)
		}
	}
}
