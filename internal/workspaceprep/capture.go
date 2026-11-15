package workspaceprep

import (
	"context"
	"database/sql"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/output"
)

// Capture creation shares the same stop gate as process start. This closes the
// intent-before-open window: an interrupted pending runner cannot create a late
// artifact after a reviewed absence observation, even if its goroutine resumes.
func (m Manager) openCapture(ctx context.Context, key, token, continuation string, e *execution.Execution) (*output.Capture, error) {
	var capture *output.Capture
	err := m.Gate(key, func() error {
		return m.Update(func(rows benchstate.Database, tx *sql.Tx) error {
			b, err := owned(rows, key, token)
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = m.preparable(b); err != nil {
				return err
			}
			if err := m.transferAdmission(ctx, tx, b); err != nil {
				return err
			}
			if continuation == "" || b.Lease.Continuation != continuation || !sameExecution(b.Lease.Execution, e) || b.Lease.Execution.Phase != "starting" {
				return errors.New("workspace capture authority was stopped or replaced")
			}
			capture, err = operationcontrol.Value(ctx, func() (*output.Capture, error) { return output.Open(e.Log, e.LogLimitBytes) })
			return err
		})
	})
	if err != nil {
		if capture != nil {
			capture.Close()
		}
		return nil, err
	}
	return capture, nil
}
