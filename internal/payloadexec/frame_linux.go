//go:build linux

package payloadexec

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
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
