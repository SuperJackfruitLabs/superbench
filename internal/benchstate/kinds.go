package benchstate

import (
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// LoadKinds inspects historical scalar state without migrating it. It is for
// compatibility preflight only; ordinary projections require CheckVersion.
func LoadKinds(r Reader, state string) (Database, error) {
	var version int
	if err := r.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version == 0 {
		records, err := Legacy(state)
		if err != nil {
			return nil, err
		}
		for _, b := range records {
			if b.Workspace != nil {
				return nil, errors.New("workspace record requires a supported SQLite state schema")
			}
		}
		return records, nil
	}
	if version < 1 || version > SchemaVersion {
		return nil, fmt.Errorf("unsupported state schema %d (expected %d)", version, SchemaVersion)
	}
	records, err := Load(r)
	if err != nil {
		return nil, err
	}
	if version < SchemaVersion {
		for _, b := range records {
			if b.Workspace != nil && (version < 14 || version == 14 && workspacepolicy.ValidateSchema14(b) != nil || version == 15 && workspacepolicy.ValidateSchema15(b) != nil || version == 16 && workspacepolicy.ValidateSchema16(b) != nil) {
				return nil, errors.New("workspace record requires current state schema")
			}
		}
	}
	return records, nil
}
