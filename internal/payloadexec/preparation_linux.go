//go:build linux

package payloadexec

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection/layout"
	"os"
	"reflect"
)

func inspectPreparation(binding helperBinding, r registeredinspection.PreparationRequest) error {
	if r.Validate() != nil {
		return ErrRequest
	}
	p := binding.policy
	l, err := loadRegistration(p, r.Key, r.LayoutID, r.PolicyDigest)
	if err != nil {
		return err
	}
	nodes, err := inspectNodes(p, l)
	if err != nil {
		return err
	}
	defer nodes.close()
	inputs, err := nodes.ObserveInputs(p, layout.Layout(l), r.Member)
	if err != nil {
		return err
	}
	defer inputs.Close()
	result := registeredinspection.Preparation{Version: registeredinspection.PreparationVersion, Layout: layout.Layout(l), Inputs: inputs.Snapshot()}
	raw, err := result.Bytes(p)
	if err != nil {
		return err
	}
	current, err := loadRegistration(p, r.Key, r.LayoutID, r.PolicyDigest)
	if err != nil || !reflect.DeepEqual(current, l) {
		return errors.Join(ErrBoundary, err)
	}
	if err = nodes.check(p, l); err != nil {
		return err
	}
	if err = inputs.Check(); err != nil {
		return err
	}
	if err = binding.recheck(); err != nil {
		return err
	}
	if err = verifyStdio(p); err != nil {
		return err
	}
	return registeredinspection.WritePreparation(os.Stdout, raw, frameTimeout)
}
