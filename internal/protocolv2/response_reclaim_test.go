package protocolv2

import (
	"encoding/json"
	"testing"
)

// A reclaiming or reclaimed workspace is a released one: checkpointed, with no
// lease, no pending generation and no acquisition offer.
func TestResponseReclaimPhasesAreReleasedOnly(t *testing.T) {
	parse := func(phase string, change func(*Workspace)) error {
		response, request, status := fixtureResponse(t, "status-mixed-complete")
		w := &response.Result.(*Status).Benches[1].(*WorkspaceInventory).Workspace
		w.Phase, w.ReadyForAcquisition = phase, false
		if change != nil {
			change(w)
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseResponse(raw, status, request)
		return err
	}
	for _, phase := range []string{"reclaiming", "reclaimed"} {
		if err := parse(phase, nil); err != nil {
			t.Fatalf("%s rejected: %v", phase, err)
		}
		for name, change := range map[string]func(*Workspace){
			"no-checkpoint": func(w *Workspace) { w.Checkpoint = nil },
			"lease":         func(w *Workspace) { w.Lease = &LeaseSummary{Holder: "worker", Mode: "cooperative"} },
			"pending": func(w *Workspace) {
				c := w.Checkpoint
				w.Pending = &Pending{GenerationID: "f" + c.GenerationID[1:], ManifestSHA256: c.ManifestSHA256, Members: c.Members, PreviousSeal: c.Seal}
			},
			"offered": func(w *Workspace) { w.ReadyForAcquisition = true },
		} {
			if parse(phase, change) == nil {
				t.Errorf("%s with %s accepted", phase, name)
			}
		}
	}
	if parse("reclaimable", nil) == nil {
		t.Fatal("an unknown phase accepted")
	}
}
