package freshrecovery

import (
	"errors"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshcleanup"
)

// The worker may clean only a daemon-owned private leaf at the recorded inode,
// with the assigned project, in a phase after Begin handed the leaf over:
// allocating (c) and begun through staged. Everything else stays with leaf and
// fixed-name removal, or parked.
func TestCleanOwnerFollowsThePhaseAndTheObservedLeaf(t *testing.T) {
	recorded := [2]uint64{device, 50}
	daemonLeaf := dirNode(50, daemonUID, daemonGID)
	want := freshcleanup.Owner{UID: daemonUID, GID: daemonGID}
	for _, phase := range []string{"allocating", "begun", "staging", "materialized_unfenced", "staged"} {
		got, err := cleanOwner(phase, recorded, assigned, daemon, device, daemonLeaf, assigned)
		if err != nil || got != want {
			t.Errorf("%s: daemon leaf refused: %+v %v", phase, got, err)
		}
	}
	mode := func(n Node, m uint32) Node { n.Mode = m; return n }
	moved := func(n Node) Node { n.Inode++; return n }
	type c struct {
		phase    string
		recorded [2]uint64
		leaf     Node
		project  uint32
		o        owners
	}
	for name, v := range map[string]c{
		"reserved":                 {"reserved", [2]uint64{}, daemonLeaf, assigned, daemon},
		"reserved recorded":        {"reserved", recorded, daemonLeaf, assigned, daemon},
		"allocating (b) root leaf": {"allocating", recorded, dirNode(50, 0, 0), assigned, daemon},
		"transferring":             {"transferring", recorded, daemonLeaf, assigned, daemon},
		"completed":                {"completed", recorded, daemonLeaf, assigned, daemon},
		"unknown phase":            {"cleaning", recorded, daemonLeaf, assigned, daemon},
		"no recorded root":         {"staged", [2]uint64{}, daemonLeaf, assigned, daemon},
		"moved leaf":               {"staged", recorded, moved(daemonLeaf), assigned, daemon},
		"other device":             {"staged", [2]uint64{device + 1, 50}, Node{Device: device + 1, Inode: 50, Mode: modeDir | 0o700, UID: daemonUID, GID: daemonGID}, assigned, daemon},
		"payload owned":            {"staged", recorded, dirNode(50, 61002, 61004), assigned, daemon},
		"daemon uid root group":    {"begun", recorded, dirNode(50, daemonUID, 0), assigned, daemon},
		"mode 0750":                {"staged", recorded, mode(daemonLeaf, modeDir|0o750), assigned, daemon},
		"sticky":                   {"staged", recorded, mode(daemonLeaf, modeDir|0o1700), assigned, daemon},
		"not a directory":          {"staged", recorded, mode(daemonLeaf, 0o100700), assigned, daemon},
		"project zero":             {"allocating", recorded, daemonLeaf, 0, daemon},
		"foreign project":          {"staged", recorded, daemonLeaf, assigned + 1, daemon},
		"root daemon identity":     {"staged", recorded, dirNode(50, 0, 0), assigned, owners{0, 0}},
	} {
		if got, err := cleanOwner(v.phase, v.recorded, assigned, v.o, device, v.leaf, v.project); !errors.Is(err, ErrRecovery) || got != (freshcleanup.Owner{}) {
			t.Errorf("%s admitted: %+v %v", name, got, err)
		}
	}
}
