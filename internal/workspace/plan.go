// Package workspace durably stages and manages aggregate checkout vectors.
package workspace

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceplan"
	"regexp"
)

type Repository = workspaceplan.Repository
type Spec = workspaceplan.Spec
type Member = workspaceplan.Member
type Plan = workspaceplan.Plan
type Planner = workspaceplan.Planner

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

func normalize(s Spec) (Spec, error) { return workspaceplan.Normalize(s) }
