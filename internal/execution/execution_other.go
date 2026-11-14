//go:build !linux

package execution

import (
	"errors"
	"os/exec"
)

type Host struct{}

var errNoFencing = errors.New("fenced execution requires Linux cgroup v2; use cooperative lease/checkpoint commands on this host")

func (Host) Create(string, string) (Identity, error) {
	return Identity{}, errNoFencing
}
func (Host) Start(*exec.Cmd, *Execution) error { return errNoFencing }
func (Host) Fence(*Execution) error            { return errNoFencing }
func (Host) Empty(*Execution) error            { return errNoFencing }
