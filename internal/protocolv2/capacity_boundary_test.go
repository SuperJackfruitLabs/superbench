package protocolv2

import (
	"errors"
	"strings"
	"testing"
)

func TestCapacityDiagnosticDoesNotWidenCancellationContract(t *testing.T) {
	target := CancelRequest{Version: Version, Operation: "cancel", TargetRequestID: "capacity-create", TargetDigest: strings.Repeat("a", 64)}
	raw := []byte(`{"version":"superbench.v2","operation":"cancel","ok":false,"error":{"code":"capacity_exhausted","message":"logical reservation capacity exhausted; needed_bytes=6500 available_bytes=6000 needed_inodes=400 available_inodes=900"}}`)
	if _, err := ParseCancelResponse(raw, 422, target); !errors.Is(err, ErrResponse) {
		t.Fatal("ordinary allocation diagnostic widened cancellation contract", err)
	}
}
