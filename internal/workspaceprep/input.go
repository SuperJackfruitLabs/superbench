package workspaceprep

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/prepinput"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

func inputs(path string) (*setup.Config, string, error) { return prepinput.Read(path) }
