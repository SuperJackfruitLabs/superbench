package quotapolicy

import (
	"encoding/json"
	"testing"
)

func TestLogicalCapacityDemandPreservesConservativeReservation(t *testing.T) {
	caps := map[string]Assignment{"bench:one": {Bytes: 1 << 30}, "bench:two": {Bytes: 1 << 30}, "bench:three": {Bytes: 1 << 30}, "bench:retired": {}, "store": {Bytes: 1 << 30}, "mirrors": {Bytes: 1 << 30}}
	const shared = 1<<30 + 1<<30 + 1<<29
	needed, err := ReservationBytes(shared, caps, true, 1<<30)
	if err != nil || needed != 13<<29 || needed <= 6<<30 {
		t.Fatal("fourth bench must exceed six GiB logical volume", needed, err)
	}
	if len(caps) != 6 || caps["bench:retired"].Bytes != 0 {
		t.Fatal("diagnostic demand changed allocations")
	}
	known, err := ReservationBytes(shared, caps, false, 0)
	if err != nil || known != 11<<29 {
		t.Fatal("known allocation counted twice", known, err)
	}
}

func TestLogicalCapacityDemandPreservesOverflowRefusal(t *testing.T) {
	if _, err := ReservationBytes(1<<60, map[string]Assignment{"bench:one": {Bytes: 1024}}, false, 0); err == nil || err.Error() != "budget overflow" {
		t.Fatal("overflow guard changed", err)
	}
}

func TestAssignmentWireBytesPreserveTransport(t *testing.T) {
	a := Assignment{Path: "/data/benches/card", ProjectID: 123, Bytes: 1024, Inodes: 128}
	raw, err := json.Marshal(a)
	if err != nil || string(raw) != `{"Path":"/data/benches/card","ProjectID":123,"Bytes":1024,"Inodes":128}` {
		t.Fatal("assignment wire bytes changed", string(raw), err)
	}
}
