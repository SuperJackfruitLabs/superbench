package remoteschema

import (
	"database/sql"
	"math"
	"strings"
	"testing"
	"time"
)

func audited(t *testing.T) *sql.DB {
	t.Helper()
	db := database(t, nil)
	upgrade(t, db)
	return db
}

func within(t *testing.T, db *sql.DB, fn func(*sql.Tx) error) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func session(serial uint64) SessionRecord {
	return SessionRecord{Serial: serial, Key: "card", BenchID: strings.Repeat("c", 32), Fingerprint: "SHA256:client", Source: "100.64.0.9:50000", Kind: "exec", CommandName: "git", StartedAt: time.Unix(1000+int64(serial), 0)}
}

func TestAuditRoundTripAndEnds(t *testing.T) {
	db := audited(t)
	at := time.Date(2026, 11, 24, 10, 0, 0, 123, time.UTC)
	c := CertificateRecord{Serial: 7, Key: "card", BenchID: strings.Repeat("c", 32), LeaseID: strings.Repeat("5", 32), Generation: 3, ExecutionID: strings.Repeat("7", 32), Holder: "worker", Fingerprint: "SHA256:client", IssuedAt: at, ValidBefore: at.Add(30 * time.Minute)}
	var open, other int64
	err := within(t, db, func(tx *sql.Tx) error {
		if err := RecordCertificate(tx, c); err != nil {
			return err
		}
		var err error
		if open, err = StartSession(tx, session(7)); err != nil {
			return err
		}
		elsewhere := session(8)
		elsewhere.BenchID = strings.Repeat("d", 32)
		other, err = StartSession(tx, elsewhere)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = within(t, db, func(tx *sql.Tx) error { return EndOpenSessions(tx, strings.Repeat("c", 32), at, "fenced") }); err != nil {
		t.Fatal(err)
	}
	certs, sessions, err := Recent(db, "card", 10)
	if err != nil || len(certs) != 1 || len(sessions) != 2 {
		t.Fatal("recent rows", certs, sessions, err)
	}
	if !certs[0].IssuedAt.Equal(at) || certs[0].Generation != 3 || certs[0].Holder != "worker" || certs[0].ExecutionID != c.ExecutionID {
		t.Fatal("certificate did not round-trip", certs[0])
	}
	for _, s := range sessions {
		switch s.ID {
		case open:
			if s.EndReason != "fenced" || !s.EndedAt.Equal(at) {
				t.Fatal("open session on the fenced bench not ended", s)
			}
		case other:
			if s.EndReason != "" || !s.EndedAt.IsZero() {
				t.Fatal("fence ended another bench's session", s)
			}
		}
	}
	if err = within(t, db, func(tx *sql.Tx) error { return EndSession(tx, other, at, math.MaxUint64, 5, "exit") }); err != nil {
		t.Fatal("ending with a huge byte count failed", err)
	}
	if err = within(t, db, func(tx *sql.Tx) error { return EndSession(tx, other, at.Add(time.Hour), 1, 9, "closed") }); err != nil {
		t.Fatal("second end failed", err)
	}
	if err = within(t, db, func(tx *sql.Tx) error { return EndSession(tx, other+100, at, 1, 1, "closed") }); err == nil {
		t.Fatal("unknown session ended")
	}
	_, sessions, _ = Recent(db, "card", 10)
	for _, s := range sessions {
		if s.ID == other && (s.EndReason != "exit" || !s.EndedAt.Equal(at) || s.BytesIn != math.MaxInt64 || s.BytesOut != 9) {
			t.Fatal("second end rewrote the first", s)
		}
	}
	for _, reason := range []string{"", "killed"} {
		if err = within(t, db, func(tx *sql.Tx) error { return EndOpenSessions(tx, "x", at, reason) }); err == nil {
			t.Fatalf("end reason %q accepted", reason)
		}
	}
}

func TestAuditRefusesValuesSQLiteCannotHold(t *testing.T) {
	db := audited(t)
	big := uint64(math.MaxInt64) + 1
	for name, fn := range map[string]func(*sql.Tx) error{
		"serial": func(tx *sql.Tx) error { return RecordCertificate(tx, CertificateRecord{Serial: big, Key: "card"}) },
		"generation": func(tx *sql.Tx) error {
			return RecordCertificate(tx, CertificateRecord{Serial: 1, Generation: big, Key: "card"})
		},
		"session-serial": func(tx *sql.Tx) error { _, err := StartSession(tx, session(big)); return err },
		"session-kind": func(tx *sql.Tx) error {
			s := session(1)
			s.Kind = "x11"
			_, err := StartSession(tx, s)
			return err
		},
	} {
		if err := within(t, db, fn); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAuditPrunesOldestEnded(t *testing.T) {
	db := audited(t)
	var first, open int64
	err := within(t, db, func(tx *sql.Tx) error {
		var err error
		if open, err = StartSession(tx, session(1)); err != nil {
			return err
		}
		for i := uint64(2); i < MaxAuditRows+7; i++ {
			id, err := StartSession(tx, session(i))
			if err != nil {
				return err
			}
			if i == 2 {
				first = id
			}
			if err = EndSession(tx, id, time.Unix(5000, 0), 1, 1, "exit"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var total, kept, oldest int
	if err = db.QueryRow("SELECT count(*), sum(id=?), sum(id=?) FROM remote_sessions", open, first).Scan(&total, &kept, &oldest); err != nil {
		t.Fatal(err)
	}
	if total != MaxAuditRows || kept != 1 || oldest != 0 {
		t.Fatal("pruning kept", total, "rows; open kept", kept, "oldest ended kept", oldest)
	}
	err = within(t, db, func(tx *sql.Tx) error {
		for i := uint64(1); i <= MaxAuditRows+5; i++ {
			if err := RecordCertificate(tx, CertificateRecord{Serial: i, Key: "card", IssuedAt: time.Unix(int64(i), 0), ValidBefore: time.Unix(int64(i), 0)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var lowest int
	if err = db.QueryRow("SELECT count(*), min(serial) FROM remote_certificates").Scan(&total, &lowest); err != nil {
		t.Fatal(err)
	}
	if total != MaxAuditRows || lowest != 6 {
		t.Fatal("certificate pruning kept", total, "lowest serial", lowest)
	}
}

func TestCommandName(t *testing.T) {
	for in, want := range map[string]string{
		"git status --short":      "git",
		"/usr/bin/env FOO=1 make": "env",
		"\x01x":                   "?",
		"ls\x7f -l":               "?",
		"caf\xe9":                 "?",
		"":                        "",
		"   ":                     "",
		strings.Repeat("n", 200):  strings.Repeat("n", 64),
		"/bin/" + strings.Repeat("é", 40) + " arg": strings.Repeat("é", 32),
	} {
		if got := CommandName(in); got != want {
			t.Errorf("CommandName(%q) = %q, want %q", in, got, want)
		}
	}
}
