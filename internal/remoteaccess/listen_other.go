//go:build !linux

package remoteaccess

import "syscall"

// freebind is Linux only; elsewhere the fleet address must exist at bind.
func freebind(string, string, syscall.RawConn) error { return nil }
