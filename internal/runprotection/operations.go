package runprotection

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

// WithOperations preserves existing lease/retirement protection and the active
// operation's independently recorded launch evidence until its durable cutoff.
func WithOperations(records map[string]*model.Bench, r operationcontrol.LegacyReader) (map[string]bool, error) {
	ids := Runs(records)
	if r == nil {
		return ids, nil
	}
	operations, err := operationcontrol.ProtectedRuns(r)
	if err != nil {
		return nil, err
	}
	for id := range operations {
		ids[id] = true
	}
	return ids, nil
}
