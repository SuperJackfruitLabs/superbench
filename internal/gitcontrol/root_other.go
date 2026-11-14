//go:build !linux

package gitcontrol

import "errors"

func Available(string) bool     { return false }
func validateRoot(string) error { return errors.New("managed Git control requires Linux cgroup v2") }
