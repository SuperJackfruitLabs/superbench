// Package registeredinspection retains read-only physical registered-layout
// observations. It cannot execute commands, create layouts or grant ownership.
package registeredinspection

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection/layout"
)

var ErrBoundary = errors.New("managed payload execution boundary refused")

type Policy = payloadpolicy.Policy
type Node = layout.Node
type Layout = layout.Layout

const MaxLayout = layout.MaxLayout
