package bench

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/history"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceprep"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacerepair"
)

func (s Store) InspectWorkspaceHistory(key string) (*workspaceprep.HistoryPlan, error) {
	if err := s.Migrate(); err != nil {
		return nil, err
	}
	return s.workspacePreparation().InspectHistory(key)
}

func (s Store) WorkspaceRepair() (workspacerepair.Manager, error) {
	if err := s.Migrate(); err != nil {
		return workspacerepair.Manager{}, err
	}
	return workspacerepair.Manager{Prep: s.workspacePreparation(), Lifecycle: s.workspaceLifecycle()}, nil
}

func (s Store) ReconcileWorkspaceHistory(p workspaceprep.HistoryPlan) (history.Record, error) {
	if err := s.Migrate(); err != nil {
		return history.Record{}, err
	}
	return s.workspacePreparation().ReconcileHistory(p)
}
