package quotahelper

import (
	"bytes"
	"fmt"
)

const CapacityCode = "capacity_exhausted"
const capacityMessage = "logical reservation capacity exhausted; needed_bytes=%d available_bytes=%d needed_inodes=%d available_inodes=%d"

// CapacityError compares the complete post-request logical reservations with the
// volume's total bytes/inodes. Available is not measured free or physical usage.
// This is diagnostic data only: it never grants an allocation or permits retry.
type CapacityError struct {
	NeededBytes, AvailableBytes   uint64
	NeededInodes, AvailableInodes uint64
}

func (e *CapacityError) Valid() bool {
	return e != nil && (e.NeededBytes > e.AvailableBytes || e.NeededInodes > e.AvailableInodes)
}
func (e *CapacityError) Message() string {
	return fmt.Sprintf(capacityMessage, e.NeededBytes, e.AvailableBytes, e.NeededInodes, e.AvailableInodes)
}
func (e *CapacityError) Error() string { return CapacityCode + ": " + e.Message() }

// capacityFailure accepts only the complete canonical line emitted by the fixed
// helper on a failed apply. Warnings, raw JSON and partial/ambiguous lines stay
// ordinary failures. No private backing error is promoted into an API message.
func capacityFailure(raw []byte) *CapacityError {
	if len(raw) > 512 || !bytes.HasPrefix(raw, []byte("bench-quota: "+CapacityCode+": ")) {
		return nil
	}
	var result CapacityError
	n, err := fmt.Sscanf(string(raw), "bench-quota: "+CapacityCode+": "+capacityMessage,
		&result.NeededBytes, &result.AvailableBytes, &result.NeededInodes, &result.AvailableInodes)
	if err != nil || n != 4 || !result.Valid() || string(raw) != "bench-quota: "+result.Error()+"\n" {
		return nil
	}
	return &result
}
