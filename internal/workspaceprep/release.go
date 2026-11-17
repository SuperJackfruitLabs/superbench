package workspaceprep

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/prepproof"
)

// ReleaseHistory releases every retained run against the complete checkpoint.
func ReleaseHistory(tx *sql.Tx, b *model.Bench) error { return prepproof.ReleaseHistory(tx, b) }

func verifyProof(tx *sql.Tx, b *model.Bench, p *model.WorkspacePreparation) error {
	return prepproof.Verify(tx, b, p)
}
