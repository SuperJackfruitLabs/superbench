package main

// isRecoveryCommand names the direct-root Fresh recovery steps. Each takes the
// original six-argument identity, refuses sudo and a policy override, and reads
// its whole bounded input before any lock: the Spec to plan, {Spec, Plan} to act.
func isRecoveryCommand(s string) bool {
	switch s {
	case "plan-remove-empty-bootstrap-leaf", "remove-empty-bootstrap-leaf", "plan-repair-bootstrap-quota", "repair-bootstrap-quota":
		return true
	}
	return false
}
