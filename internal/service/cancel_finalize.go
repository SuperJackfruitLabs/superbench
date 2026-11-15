package service

import "forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"

// finalizeOperation runs after the original completed receipt commit, including
// restart replay of that cutoff. Its failure never reopens the original request
// or rewrites the historical response; retry only this metadata reconciliation.
func (s *Server) finalizeOperation(i operationcontrol.Intent) error {
	manager, err := s.config.Store.OperationCancellation()
	if err != nil {
		return err
	}
	if err = manager.Finish(i, func() error { return s.receipts.FinalizationCutoff(i) }); err != nil {
		return err
	}
	return s.receipts.MarkCoreFinalized(i)
}
