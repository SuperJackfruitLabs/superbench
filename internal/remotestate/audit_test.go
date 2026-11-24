package remotestate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteschema"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

// A real lease's token never reaches claims, audit rows or status output.
func TestAuditNeverStoresTokens(t *testing.T) {
	f := newFixture(t)
	if len(f.token) < 32 {
		t.Fatal("fixture has no real lease token")
	}
	g := f.grant(t)
	a, err := f.m.Admit(g.Claims)
	if err != nil {
		t.Fatal(err)
	}
	now := f.m.Now()
	if err = f.m.RecordCertificate(remoteschema.CertificateRecord{Serial: 11, Key: a.Key, BenchID: a.BenchID, LeaseID: g.Claims.LeaseID, Generation: g.Claims.Generation, ExecutionID: g.Claims.ExecutionID, Holder: a.Holder, Fingerprint: "SHA256:client", IssuedAt: now, ValidBefore: now.Add(remoteca.WriterValidity)}); err != nil {
		t.Fatal(err)
	}
	ended, err := f.m.SessionStarted(a, remoteschema.SessionRecord{Serial: 11, Fingerprint: "SHA256:client", Source: "100.64.0.9:50000", Kind: "exec", CommandName: remoteschema.CommandName("git push " + f.token)})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.SessionEnded(ended, 1, 2, "exit"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.SessionStarted(a, remoteschema.SessionRecord{Serial: 11, Kind: "sftp"}); err != nil {
		t.Fatal(err)
	}
	status, err := f.m.Status("card", 10)
	if err != nil || len(status.Remote) != 1 || len(status.Certificates) != 1 || len(status.Sessions) != 2 {
		t.Fatal("status", status, err)
	}
	for name, v := range map[string]any{"grant": g, "admission": a, "status": status} {
		if strings.Contains(fmt.Sprintf("%+v", v), f.token) {
			t.Errorf("%s holds the lease token", name)
		}
	}
	if err = f.m.Fence(context.Background(), "card", f.token, false); err != nil {
		t.Fatal(err)
	}
	// Dump every column of both tables straight from the state database.
	err = statestore.Read(f.store.State, func(db *sql.DB) error {
		for _, table := range []string{"remote_certificates", "remote_sessions"} {
			rows, err := db.Query("SELECT * FROM " + table)
			if err != nil {
				return err
			}
			columns, _ := rows.Columns()
			n := 0
			for rows.Next() {
				n++
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					rows.Close()
					return err
				}
				if strings.Contains(fmt.Sprint(values...), f.token) {
					t.Errorf("%s row holds the lease token", table)
				}
			}
			rows.Close()
			if n == 0 {
				return errors.New(table + " is empty: nothing was checked")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartRefusedOnceRevoked(t *testing.T) {
	f := newFixture(t)
	g := f.grant(t)
	a, err := f.m.Admit(g.Claims)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.Fence(context.Background(), "card", f.token, false); err != nil {
		t.Fatal(err)
	}
	if id, err := f.m.SessionStarted(a, remoteschema.SessionRecord{Serial: 1, Kind: "shell"}); !errors.Is(err, ErrRevoked) || id != 0 {
		t.Fatal("session started on a fenced execution", id, err)
	}
	if s, _ := f.m.Status("card", 10); len(s.Sessions) != 0 {
		t.Fatal("refused session recorded", s.Sessions)
	}
}
