//go:build !linux

package freshtree

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"

// The payload helper and its account exist only on Linux.
func installed(*model.Bench, uint32) (Tree, error) { return nil, nil }
