//go:build linux

package payloadbootstrap

// requireMaterializePhase is the phase-only precondition used by Materialize.
// It grants no installed-policy, operation, or filesystem authority.
func (i intent) requireMaterializePhase() error {
	if i.Phase != "begun" {
		return ErrBootstrap
	}
	return nil
}
