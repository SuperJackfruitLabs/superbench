package operationcontrol

import "database/sql"

// Finish retires only the exact operation exclusion after its caller proves the
// original historical receipt durably finalized. It never changes lease tokens,
// continuations, setup proofs or ownership; a subsequent request has a new epoch.
func Finish(tx *sql.Tx, i Intent) error {
	if !i.Valid() {
		return ErrAuthority
	}
	found, err := Get(tx, i.Target)
	if err == sql.ErrNoRows {
		return nil
	} // No engine effect was ever admitted.
	if err != nil {
		return err
	}
	if found.Intent != i {
		return ErrAuthority
	}
	if found.Phase == "finished" {
		return nil
	}
	if found.Phase != "admitted" && found.Phase != "bound" {
		return ErrAuthority
	}
	result, err := tx.Exec(`UPDATE workspace_operations SET phase='finished' WHERE id=? AND epoch=? AND phase=?`, i.Target.ID(), i.Epoch, found.Phase)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAuthority
	}
	saved, err := Get(tx, i.Target)
	if err != nil {
		return err
	}
	if saved.Phase != "finished" {
		return ErrAuthority
	}
	return nil
}
