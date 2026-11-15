package workspace

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacematerialize"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceplan"
)

type Materializer = workspacematerialize.Materializer

func validateMaterialization(p Plan) (string, error) { return workspaceplan.ValidateMaterialization(p) }
func branchNamespaceConflict(a, b string) bool       { return workspaceplan.BranchNamespaceConflict(a, b) }
func validateWorktreeOwnership(raw string, m Member) error {
	return workspacematerialize.ValidateWorktreeOwnership(raw, m)
}
func physicalDirectory(p string, optional bool) error {
	return workspacematerialize.PhysicalDirectory(p, optional)
}
