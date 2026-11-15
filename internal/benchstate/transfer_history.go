package benchstate

import "errors"

// Existing v1 journals remain byte-for-byte intact. A historical database may
// not acquire future missing-checkout authority by injecting a v2 plan.
func historicalTransferPlans(r Reader) error {
	var count int
	if err := r.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='workspace_ref_transfers'").Scan(&count); err != nil || count == 0 {
		return err
	}
	if err := r.QueryRow("SELECT count(*) FROM workspace_ref_transfers WHERE json_valid(plan)=0 OR json_extract(plan,'$.Version') IS NULL OR json_extract(plan,'$.Version')!='superbench.workspace-transfer.v1'").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("missing-checkout transfer journal requires current state schema")
	}
	return nil
}
