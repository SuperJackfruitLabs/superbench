package remotestate

import (
	"database/sql"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
)

// RecordCertificate audits one issued certificate.
func (m Manager) RecordCertificate(r remoteschema.CertificateRecord) error {
	if err := m.valid(); err != nil {
		return err
	}
	return m.Prep.Update(func(_ benchstate.Database, tx *sql.Tx) error { return remoteschema.RecordCertificate(tx, r) })
}

// SessionStarted re-admits the claims and records the session in the same
// transaction, so no session row starts after a fence has ended the others.
// The admission names the key and bench; the record's own are ignored.
func (m Manager) SessionStarted(a Admission, r remoteschema.SessionRecord) (int64, error) {
	if err := m.valid(); err != nil {
		return 0, err
	}
	var id int64
	err := m.Prep.Update(func(rows benchstate.Database, tx *sql.Tx) error {
		b, err := admitted(rows, a.Claims)
		if err != nil {
			return err
		}
		r.Key, r.BenchID = b.Key, b.Workspace.ID
		if r.StartedAt.IsZero() {
			r.StartedAt = m.now()
		}
		id, err = remoteschema.StartSession(tx, r)
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SessionEnded records a session's end and byte counts. A session a fence
// already ended keeps "fenced" and gains only the counts.
func (m Manager) SessionEnded(id int64, in, out uint64, reason string) error {
	if err := m.valid(); err != nil {
		return err
	}
	return m.Prep.Update(func(_ benchstate.Database, tx *sql.Tx) error {
		return remoteschema.EndSession(tx, id, m.now(), in, out, reason)
	})
}
