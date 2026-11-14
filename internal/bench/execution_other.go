//go:build !linux

package bench

import (
	"errors"
	"os/exec"
)

type hostExecutor struct{}

var errNoFencing = errors.New("fenced execution requires Linux cgroup v2; use cooperative lease/checkpoint commands on this host")

func (hostExecutor) Create(string, string) (executionIdentity, error) {
	return executionIdentity{}, errNoFencing
}
func (hostExecutor) Start(*exec.Cmd, *Execution) error { return errNoFencing }
func (hostExecutor) Fence(*Execution) error            { return errNoFencing }
func (hostExecutor) Empty(*Execution) error            { return errNoFencing }
