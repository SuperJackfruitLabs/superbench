package payloadbootstrap

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"

// reclaimable admits, for a reclaim manager only, the intent of an earlier
// create under the same storage authority once its bootstrap has completed.
// The reclaim identity is never the create's, which may name a binary since
// upgraded and is never revived.
func reclaimable(reclaim bool, recorded, current quotaoperation.Identity, phase string) bool {
	return reclaim && recorded.Valid() && recorded.Authority == current.Authority && (phase == "completed" || phase == "reclaiming" || phase == "reclaimed")
}

// reclaimPhase is the only forward path a reclaim moves a completed intent
// along: completed, then reclaiming (retained before any effect), then
// reclaimed. Repeating the current phase is an exact retry.
func reclaimPhase(from, to string) bool {
	return from == to && (to == "reclaiming" || to == "reclaimed") || from == "completed" && to == "reclaiming" || from == "reclaiming" && to == "reclaimed"
}
