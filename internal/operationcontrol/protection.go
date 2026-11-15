package operationcontrol

// ProtectedRuns retains launch evidence through the physical-release to receipt
// cutoff window. Inactive epochs no longer pin ordinary released history.
// A corrupt or oversized inventory refuses collection rather than losing proof.
func ProtectedRuns(r LegacyReader) (map[string]bool, error) {
	if r == nil {
		return nil, ErrAuthority
	}
	var orphans int
	if err := r.QueryRow(`SELECT count(*) FROM workspace_operation_runs r LEFT JOIN workspace_operations o ON o.id=r.operation_id WHERE o.id IS NULL OR r.epoch!=o.epoch`).Scan(&orphans); err != nil {
		return nil, err
	}
	if orphans != 0 {
		return nil, ErrAuthority
	}
	rows, err := r.Query(`SELECT id,`+columns+` FROM workspace_operations WHERE phase IN ('admitted','bound') ORDER BY id LIMIT ?`, MaxOperations+1)
	if err != nil {
		return nil, err
	}
	var active []Binding
	for rows.Next() {
		b, e := scanBinding(rows, true)
		if e != nil {
			rows.Close()
			return nil, e
		}
		active = append(active, b)
		if len(active) > MaxOperations {
			rows.Close()
			return nil, ErrAuthority
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	ids := map[string]bool{}
	for _, b := range active {
		runs, err := Runs(r, b.Intent)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			ids[run.Execution.ID] = true
		}
	}
	return ids, nil
}
