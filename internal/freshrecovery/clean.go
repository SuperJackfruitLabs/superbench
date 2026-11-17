package freshrecovery

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshcleanup"

// cleanable reports the phases whose leaked tree the daemon-uid worker may
// clean. reserved and allocating (a) and (b) are root-owned and use leaf and
// fixed-name removal; transferring is mixed and stays parked; completed is not
// a leak.
func cleanable(phase string) bool {
	switch phase {
	case "allocating", "begun", "staging", "materialized_unfenced", "staged":
		return true
	}
	return false
}

// cleanOwner derives the only owner a cleanup approval may name: the daemon,
// for a cleanable phase whose leaf is observed at the recorded inode on the
// pinned device, as a daemon-owned private (0700) directory carrying the
// assigned project. For allocating this is case (c): Begin had already handed
// the leaf to the daemon. Any other leaf or phase is refused.
func cleanOwner(phase string, recorded [2]uint64, assigned uint32, o owners, device uint64, leaf Node, project uint32) (freshcleanup.Owner, error) {
	if !cleanable(phase) || o.daemonUID == 0 || o.daemonGID == 0 || recorded == ([2]uint64{}) || recorded != [2]uint64{leaf.Device, leaf.Inode} {
		return freshcleanup.Owner{}, ErrRecovery
	}
	if leaf.Device != device || !private(leaf) || !o.daemon(leaf) || project != assigned {
		return freshcleanup.Owner{}, ErrRecovery
	}
	return freshcleanup.Owner{UID: o.daemonUID, GID: o.daemonGID}, nil
}
