package worktreetransfer

import (
	"context"
	"errors"
	"reflect"
)

// Preflight verifies an approved present checkout without detaching it. It also
// accepts the exact prior same-commit detachment when a whole transfer retries.
// The caller must retain source exclusion across every member and later Apply.
func (m Manager) Preflight(ctx context.Context, p Plan) error {
	c, cancel, err := m.bounded(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if p.Version != Version {
		return errors.New("unsupported transfer plan")
	}
	current, err := m.inspect(c, p.Checkout, p.Common, p.TaskRef, p.Commit, false)
	if err != nil {
		current, err = m.inspect(c, p.Checkout, p.Common, p.TaskRef, p.Commit, true)
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, p) {
		return errors.New("transfer snapshot changed during whole-vector preflight")
	}
	tip, err := m.git(c, p.Common, "rev-parse", "--verify", p.TaskRef)
	if err != nil || tip != p.Commit {
		return errors.New("task ref changed before transfer")
	}
	return c.Err()
}
