//go:build linux

package payloadexec

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/registeredinspection"
	"os"
	"time"
)

var ErrFrame = payloadwire.ErrFrame

const frameTimeout = payloadwire.FrameTimeout

func readFrame(input *os.File, timeout time.Duration) (Request, error) {
	return payloadwire.ReadFrame(input, timeout)
}
func readInvocation(input *os.File, timeout time.Duration) (*InspectionRequest, Request, error) {
	return payloadwire.ReadInvocation(input, timeout)
}
func readAnyInvocation(input *os.File, timeout time.Duration) (*InspectionRequest, *registeredinspection.PreparationRequest, Request, error) {
	return payloadwire.ReadAnyInvocation(input, timeout)
}
