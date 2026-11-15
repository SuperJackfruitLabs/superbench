package operationcontrol

import (
	"context"
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// AdmitContext commits an operation epoch before source selection or mirror
// refresh. It does not adopt an existing owner's capability: BindContext must
// follow the engine's independent exact-owner checks.
func AdmitContext(ctx context.Context, tx *sql.Tx, key, benchID string) error {
	i, ok := FromContext(ctx)
	if !ok {
		_, err := scan(tx.QueryRow(`SELECT id,`+columns+` FROM workspace_operations WHERE key=? AND phase IN ('admitted','bound')`, key))
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		return ErrBusy
	}
	if i.Key != key {
		return ErrAuthority
	}
	if err := Check(ctx); err != nil {
		return err
	}
	old, err := Get(tx, i.Target)
	if err == nil && old.Phase == "bound" {
		if old.Intent != i || old.BenchID != benchID || old.Control != ControlFromContext(ctx) {
			return ErrAuthority
		}
		return nil
	}
	return BeginWithControl(tx, i, benchID, ControlFromContext(ctx))
}

// BindContext must share the transaction that saves or independently validates
// the operation's owner. An already bound exact epoch is idempotent.
func BindContext(ctx context.Context, tx *sql.Tx, b *model.Bench) error {
	i, ok := FromContext(ctx)
	if !ok {
		return RequireContext(tx, b, nil)
	}
	if err := Check(ctx); err != nil {
		return err
	}
	old, err := Get(tx, i.Target)
	if err != nil {
		return err
	}
	if old.Control != ControlFromContext(ctx) {
		return ErrAuthority
	}
	if old.Phase == "bound" {
		return Require(tx, i, b)
	}
	return Bind(tx, i, b)
}
func GuardContext(ctx context.Context, r Reader, b *model.Bench) error {
	i, ok := FromContext(ctx)
	if !ok {
		return RequireContext(r, b, nil)
	}
	if err := Check(ctx); err != nil {
		return err
	}
	old, err := Get(r, i.Target)
	if err != nil {
		return err
	}
	if old.Control != ControlFromContext(ctx) {
		return ErrAuthority
	}
	return Require(r, i, b)
}
