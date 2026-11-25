//go:build !linux

package remoteaccess

import "errors"

// Sessions use Linux mount namespaces; elsewhere remote access never starts.
func probeMountSetattr() error { return errors.New("remote sessions need Linux") }
