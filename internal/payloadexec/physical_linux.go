//go:build linux

package payloadexec

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"os"
)

func openPhysicalDirectory(path string, daemonUID uint32) (*os.File, error) {
	return registeredinspection.OpenPhysicalDirectory(path, daemonUID)
}
