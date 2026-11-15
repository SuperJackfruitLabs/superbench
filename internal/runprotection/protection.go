// Package runprotection selects durable run evidence excluded from collection.
package runprotection

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"

func Runs(records map[string]*model.Bench) map[string]bool {
	ids := map[string]bool{}
	for _, b := range records {
		if b == nil {
			continue
		}
		if b.Lease != nil {
			for _, id := range b.Lease.RetainedRuns {
				ids[id] = true
			}
			if b.Lease.Execution != nil {
				ids[b.Lease.Execution.ID] = true
			}
		}
		if b.Workspace != nil {
			if r := b.Workspace.InitializationRepair; r != nil && b.Lease != nil && b.Workspace.Phase != "ready" {
				for _, id := range r.RetiredRuns {
					ids[id] = true
				}
			}
			if t := b.Workspace.Transfer; t != nil && t.Recovery != nil {
				for _, r := range t.Recovery.Runs {
					ids[r.ID] = true
				}
			}
		}
	}
	return ids
}
