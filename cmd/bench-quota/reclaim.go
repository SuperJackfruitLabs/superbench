package main

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

var errReclaimIdentity = errors.New("reclaim-payload identity is not the workspace's reclaim identity")

// checkReclaimIdentity recomputes the reclaim operation's identity from the
// Spec's key and BID, as the daemon's transport derives it, and requires the
// passed identity to be exactly it: a reclaim cannot run under a create's or
// any other operation's reservation. The state identity, which only the
// daemon can observe, and the authority and binary, which Open checks
// against the installed ones, are the passed values.
func checkReclaimIdentity(s payloadbootstrap.Spec, i quotaoperation.Identity) error {
	if i != quotahelper.ReclaimIdentity(s.Key, s.ID, i.Authority, i.BinarySHA256, i.StateIdentity) {
		return errReclaimIdentity
	}
	return nil
}
