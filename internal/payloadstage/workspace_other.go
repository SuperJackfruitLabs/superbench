//go:build !linux

package payloadstage

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/stageexec"
	"os/exec"
)

func workspace(serviceidentity.Authority, *model.Bench, string, bool, []byte, bool) stageexec.PrepareTransport {
	return func(*exec.Cmd, *execution.Execution) (stageexec.Transport, error) { return nil, ErrRegistration }
}
