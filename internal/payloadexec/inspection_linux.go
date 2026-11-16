//go:build linux

package payloadexec

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection/layout"
	"os"
	"reflect"
	"time"
)

type inspectedNodes struct{ *registeredinspection.Nodes }

func inspectNodes(p Policy, l Layout) (*inspectedNodes, error) {
	n, err := registeredinspection.InspectNodes(p, layout.Layout(l))
	if err != nil {
		return nil, err
	}
	return &inspectedNodes{n}, nil
}
func (n *inspectedNodes) close()                         { n.Close() }
func (n *inspectedNodes) check(p Policy, l Layout) error { return n.Check(p, layout.Layout(l)) }

func inspectInstalled(binding helperBinding, r InspectionRequest) error {
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
	raw, err := l.Bytes(p)
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
	if err = binding.recheck(); err != nil {
		return err
	}
	if err = verifyStdio(p); err != nil {
		return err
	}
	nodes.close()
	return writeInspection(os.Stdout, raw, frameTimeout)
}

func writeInspection(output *os.File, raw []byte, timeout time.Duration) error {
	return registeredinspection.WriteLayout(output, raw, timeout)
}
