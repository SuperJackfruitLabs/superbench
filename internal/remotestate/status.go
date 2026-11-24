package remotestate

import (
	"database/sql"
	"sort"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
)

// RemoteExecution names a recorded remote execution by lease identity only.
type RemoteExecution struct {
	Key, BenchID, Holder, LeaseID, ExecutionID string
	Generation                                 uint64
}
type Status struct {
	Remote       []RemoteExecution
	Certificates []remoteschema.CertificateRecord
	Sessions     []remoteschema.SessionRecord
}

// Status lists remote executions and the newest audit rows, for one key or,
// when key is empty, every key. Its body only reads: Prep.Read has no SQL
// handle for the audit tables.
func (m Manager) Status(key string, limit int) (Status, error) {
	if err := m.valid(); err != nil {
		return Status{}, err
	}
	var s Status
	err := m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		for k, b := range rows {
			if key != "" && k != key || b == nil || b.Workspace == nil || b.Lease == nil || b.Lease.Execution == nil || b.Lease.Execution.Kind != execution.KindRemote {
				continue
			}
			s.Remote = append(s.Remote, RemoteExecution{Key: k, BenchID: b.Workspace.ID, Holder: b.Lease.Holder, LeaseID: b.Lease.ID, ExecutionID: b.Lease.Execution.ID, Generation: b.Lease.Generation})
		}
		sort.Slice(s.Remote, func(i, j int) bool { return s.Remote[i].Key < s.Remote[j].Key })
		var err error
		s.Certificates, s.Sessions, err = remoteschema.Recent(tx, key, limit)
		return err
	})
	if err != nil {
		return Status{}, err
	}
	return s, nil
}
