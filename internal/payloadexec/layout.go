package payloadexec

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection/layout"

const MaxLayout = layout.MaxLayout

type Node = layout.Node
type LayoutMember = layout.LayoutMember
type Layout layout.Layout

func policyDigest(p Policy) (string, error)     { return layout.PolicyDigest(p) }
func (l Layout) Validate(p Policy) error        { return layout.Layout(l).Validate(p) }
func (l Layout) Bytes(p Policy) ([]byte, error) { return layout.Layout(l).Bytes(p) }
func ParseLayout(p Policy, raw []byte) (Layout, error) {
	l, err := layout.ParseLayout(p, raw)
	return Layout(l), err
}
func (l Layout) paths(p Policy, name string) (string, string, string, string, error) {
	return layout.Layout(l).Paths(p, name)
}
