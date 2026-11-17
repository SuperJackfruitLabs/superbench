//go:build !linux

package freshtree

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"

// The payload helper and its account exist only on Linux, so a registration
// here can never be served: Select fails closed.
func installed(*model.Bench) (Tree, error) { return nil, ErrUnavailable }
