package benchstate

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/handoffjournal"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestReservedHandoffKeyCannotBecomeOrdinaryRegistration(t *testing.T) {
	db := oldDatabase(t, SchemaVersion, aggregate())
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = handoffjournal.Schema(tx); err != nil {
		t.Fatal(err)
	}
	sid, err := handoffjournal.StateID(tx)
	if err != nil {
		t.Fatal(err)
	}
	o := handoffjournal.Offer{Version: handoffjournal.OfferVersion, Destination: crosshost.Destination{HostID: strings.Repeat("1", 64), StateID: sid, WorkspaceID: strings.Repeat("2", 32), Key: "reserved", CreationID: "cross-import"}, QuotaBytes: 32 << 20, CreatedAt: time.Now().UTC()}
	if err = handoffjournal.ReserveOffer(tx, o); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = Update(tx, func(rows Database, _ *sql.Tx) error {
		rows["reserved"] = &model.Bench{RegistrationID: "0123456789abcdef0123456789abcdef", Key: "reserved", Path: "/fixed/benches/reserved", Mirror: "/fixed/mirrors/repo", Repo: "repo", Branch: "card/reserved"}
		return nil
	})
	if err == nil {
		t.Fatal("ordinary registration consumed a durable cross-host reservation")
	}
}

func TestSchema29Preserves28AndRejectsFutureJournalOrMarker(t *testing.T) {
	t.Run("preserve", func(t *testing.T) {
		db := oldDatabase(t, 28, aggregate())
		var before, after string
		if err := db.QueryRow(`SELECT record FROM benches`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err := migrateForTest(db, t.TempDir(), nil); err != nil {
			t.Fatal(err)
		}
		assertVersion(t, db, SchemaVersion)
		if err := db.QueryRow(`SELECT record FROM benches`).Scan(&after); err != nil || before != after {
			t.Fatal("changed old row", err)
		}
		if _, err := handoffjournal.StateID(db); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("future-table", func(t *testing.T) {
		db := oldDatabase(t, 28, aggregate())
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = handoffjournal.Schema(tx); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err = migrateForTest(db, t.TempDir(), nil); err == nil {
			t.Fatal("adopted future identity")
		}
		assertVersion(t, db, 28)
		if _, err = LoadKinds(db, t.TempDir()); err == nil {
			t.Fatal("historical readonly adopted future authority")
		}
	})
	t.Run("future-marker", func(t *testing.T) {
		b := outgoing(t)
		b.Workspace.Transfer.CrossHost = &model.WorkspaceHandoff{HostID: strings.Repeat("1", 64), StateID: strings.Repeat("2", 32), ArtifactDigest: strings.Repeat("3", 64)}
		db := oldDatabase(t, 28, b)
		if err := migrateForTest(db, t.TempDir(), nil); err == nil {
			t.Fatal("adopted future marker")
		}
		assertVersion(t, db, 28)
	})
}
