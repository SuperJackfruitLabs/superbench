package freshtree

import (
	"context"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/prepinput"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
)

// These member accessors keep the legacy direct read unchanged when s selects
// no tree, and otherwise use only the selected payload route.

// Inputs binds preparation to the member's parsed config and manifests.
func Inputs(ctx context.Context, s Selector, state string, b *model.Bench, member model.WorkspaceMember) (*setup.Config, string, error) {
	tree, err := Resolve(s, state, b)
	if err != nil {
		return nil, "", err
	}
	if tree == nil {
		return prepinput.Read(member.Path)
	}
	in, err := tree.Inputs(ctx, b, member.Name)
	if err != nil {
		return nil, "", err
	}
	return Config(in)
}

// FrozenCommand selects lockfile-driven setup for a member without hooks.
func FrozenCommand(ctx context.Context, s Selector, state string, b *model.Bench, member model.WorkspaceMember, c *setup.Config, data string, offline bool) ([]string, error) {
	tree, err := Resolve(s, state, b)
	if err != nil {
		return nil, err
	}
	if tree == nil {
		return setup.FrozenCommandFor(member.Path, cachepolicy.Select(data, member.Path, c.CacheSettings()), offline)
	}
	in, err := tree.Inputs(ctx, b, member.Name)
	if err != nil {
		return nil, err
	}
	return nil, Frozen(in)
}

// StageTransport is nil for a legacy checkout, which the daemon runs directly.
func StageTransport(s Selector, state string, b *model.Bench, member string) (stageexec.PrepareTransport, error) {
	tree, err := Resolve(s, state, b)
	if err != nil || tree == nil {
		return nil, err
	}
	return tree.Transport(b, member)
}

// Selected reports whether b is a Fresh tree under s.
func Selected(s Selector, state string, b *model.Bench) (bool, error) {
	tree, err := Resolve(s, state, b)
	return tree != nil, err
}
