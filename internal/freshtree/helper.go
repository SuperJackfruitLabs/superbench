package freshtree

import (
	"encoding/binary"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
)

// ErrHelper reports a refused or malformed payload helper observation.
var ErrHelper = errors.New("fresh workspace payload helper observation refused")

// bindPreparation accepts one framed helper reply only when it describes b's
// exact workspace: its key, layout ID, installed policy digest, the requested
// member, and the complete registered member vector in order.
func bindPreparation(p payloadpolicy.Policy, digest string, b *model.Bench, member string, reply []byte) (registeredinspection.Preparation, error) {
	var zero registeredinspection.Preparation
	if len(reply) < 4 || int(binary.BigEndian.Uint32(reply[:4])) != len(reply)-4 {
		return zero, ErrHelper
	}
	v, err := registeredinspection.ParsePreparation(p, reply[4:])
	if err != nil || v.Layout.Key != b.Key || v.Layout.ID != b.Workspace.ID || v.Layout.PolicyDigest != digest || v.Inputs.Member != member || len(v.Layout.Members) != len(b.Workspace.Members) {
		return zero, errors.Join(ErrHelper, err)
	}
	for i, m := range v.Layout.Members {
		if m.Name != b.Workspace.Members[i].Name {
			return zero, ErrHelper
		}
	}
	return v, nil
}

// rootResult refuses a clean observation of a root with unregistered entries.
func rootResult(v registeredinspection.Preparation, clean bool) error {
	if clean && v.Unregistered {
		return model.ErrDirty
	}
	return nil
}
