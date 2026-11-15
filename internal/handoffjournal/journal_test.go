package handoffjournal

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/crosshost"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func transaction(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func database(t *testing.T) *sql.DB {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "core.db"), false)
	must(t, err)
	t.Cleanup(func() { db.Close() })
	must(t, transaction(db, Schema))
	return db
}
func seal(t *testing.T, a crosshost.Artifact) crosshost.Artifact {
	t.Helper()
	raw, err := crosshost.Seal(a)
	must(t, err)
	a, err = crosshost.Parse(raw)
	must(t, err)
	return a
}
func fixture(t *testing.T) (*sql.DB, *sql.DB, Offer, Export) {
	t.Helper()
	source, dest := database(t), database(t)
	sid, err := StateID(source)
	must(t, err)
	did, err := StateID(dest)
	must(t, err)
	o := Offer{Version: OfferVersion, Destination: crosshost.Destination{
		HostID: strings.Repeat("c", 64), StateID: did, WorkspaceID: strings.Repeat("d", 32),
		Key: "card_same", CreationID: "import-1"}, QuotaBytes: 32 << 20, CreatedAt: time.Now().UTC()}
	approval := []byte(`{"Version":"superbench.cross-host-export-approval.v1","sourceRow":"reviewed"}`)
	sum := sha256.Sum256(approval)
	a := crosshost.Artifact{Version: crosshost.Version,
		Source: crosshost.Source{HostID: strings.Repeat("a", 64), StateID: sid, WorkspaceID: strings.Repeat("b", 32),
			Key: "card_same", TransferID: "export-1", ApprovalDigest: hex.EncodeToString(sum[:])},
		Destination: o.Destination, CheckpointRepository: "a", Seal: strings.Repeat("1", 40), ManifestDigest: strings.Repeat("2", 64),
		Members: []crosshost.Member{{Name: "a", Repository: "forge.example/app", Branch: "card/original", IntegrationBase: "main", Commit: strings.Repeat("3", 40)}}}
	return source, dest, o, Export{Artifact: seal(t, a), Approval: approval, Phase: "started"}
}

func TestStateIdentityIsStableAndMissingAuthorityCannotBeRecreated(t *testing.T) {
	db := database(t)
	id, err := StateID(db)
	must(t, err)
	must(t, transaction(db, Schema))
	after, err := StateID(db)
	must(t, err)
	if after != id {
		t.Fatal("migration changed state identity")
	}
	_, err = db.Exec(`DELETE FROM workspace_handoff_identity`)
	must(t, err)
	if transaction(db, Schema) == nil {
		t.Fatal("lost state identity silently recreated")
	}
	if _, err := StateID(db); err == nil {
		t.Fatal("missing authority read as valid")
	}
	_, err = db.Exec(`DROP TABLE workspace_handoff_identity`)
	must(t, err)
	if transaction(db, Schema) == nil {
		t.Fatal("orphan transfer tables adopted under a fresh identity")
	}
}

func TestOfferReplayCannotRedirectOrRecycleDestination(t *testing.T) {
	_, db, o, _ := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, o) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, o) }))
	for _, change := range []func(*Offer){
		func(o *Offer) { o.QuotaBytes *= 2 }, func(o *Offer) { o.Destination.HostID = strings.Repeat("e", 64) },
		func(o *Offer) { o.Destination.StateID = strings.Repeat("f", 32) }, func(o *Offer) { o.Destination.WorkspaceID = strings.Repeat("e", 32) },
		func(o *Offer) { o.Destination.CreationID = "import-2" }, func(o *Offer) { o.Destination.Key = "other" },
	} {
		changed := o
		change(&changed)
		if transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, changed) }) == nil {
			t.Fatal("offer identity rebound")
		}
	}
}

func TestExportHasOneDestinationAndIrreversibleRetirement(t *testing.T) {
	db, _, _, e := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error { return StartExport(tx, e) }))
	changed := e
	changed.Artifact.Destination.HostID = strings.Repeat("e", 64)
	changed.Artifact = seal(t, changed.Artifact)
	if transaction(db, func(tx *sql.Tx) error { return StartExport(tx, changed) }) == nil {
		t.Fatal("existing transfer redirected")
	}
	changed.Artifact.Source.TransferID = "export-2"
	changed.Artifact = seal(t, changed.Artifact)
	if transaction(db, func(tx *sql.Tx) error { return StartExport(tx, changed) }) == nil {
		t.Fatal("same source exported twice")
	}
	tx, err := db.Begin()
	must(t, err)
	must(t, RelinquishExport(tx, e.Artifact.Source.TransferID, e.Artifact.Digest))
	must(t, tx.Rollback())
	before, err := ExportByID(db, e.Artifact.Source.TransferID)
	must(t, err)
	if before.Phase != "started" {
		t.Fatal("rollback published retirement")
	}
	must(t, transaction(db, func(tx *sql.Tx) error { return RelinquishExport(tx, e.Artifact.Source.TransferID, e.Artifact.Digest) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return StartExport(tx, e) }))
	after, err := ExportByID(db, e.Artifact.Source.TransferID)
	must(t, err)
	if after.Phase != "relinquished" {
		t.Fatal("lost-reply replay reset source retirement")
	}
	if transaction(db, func(tx *sql.Tx) error {
		return RelinquishExport(tx, e.Artifact.Source.TransferID, strings.Repeat("f", 64))
	}) == nil {
		t.Fatal("changed digest accepted")
	}
	_, err = db.Exec(`UPDATE workspace_handoff_exports SET approval='{}'`)
	must(t, err)
	if _, err := ExportByID(db, e.Artifact.Source.TransferID); err == nil {
		t.Fatal("changed source approval accepted")
	}
}

func TestImportRequiresOfferAndNeverDuplicatesOrReplacesSource(t *testing.T) {
	_, db, o, e := fixture(t)
	if transaction(db, func(tx *sql.Tx) error { return AcceptImport(tx, e.Artifact) }) == nil {
		t.Fatal("import without durable offer")
	}
	must(t, transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, o) }))
	wrong := e.Artifact
	wrong.Destination.HostID = strings.Repeat("e", 64)
	wrong = seal(t, wrong)
	if transaction(db, func(tx *sql.Tx) error { return AcceptImport(tx, wrong) }) == nil {
		t.Fatal("re-sealed foreign destination accepted")
	}
	must(t, transaction(db, func(tx *sql.Tx) error { return AcceptImport(tx, e.Artifact) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return CompleteImport(tx, o.Destination.CreationID, e.Artifact.Digest) }))
	must(t, transaction(db, func(tx *sql.Tx) error { return AcceptImport(tx, e.Artifact) }))
	got, err := ImportByID(db, o.Destination.CreationID)
	must(t, err)
	if got.Phase != "completed" {
		t.Fatal("replay reset completed import")
	}
	other := o
	other.Destination.Key = "other"
	other.Destination.CreationID = "import-2"
	other.Destination.WorkspaceID = strings.Repeat("e", 32)
	must(t, transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, other) }))
	duplicate := e.Artifact
	duplicate.Destination = other.Destination
	duplicate = seal(t, duplicate)
	if transaction(db, func(tx *sql.Tx) error { return AcceptImport(tx, duplicate) }) == nil {
		t.Fatal("source accepted a second time under a new offer")
	}
	altered := e.Artifact
	altered.Source.WorkspaceID = strings.Repeat("f", 32)
	altered = seal(t, altered)
	if transaction(db, func(tx *sql.Tx) error { return AcceptImport(tx, altered) }) == nil {
		t.Fatal("existing import replaced by another source")
	}
}

func TestIgnoredWritesDoNotAcknowledgeAuthority(t *testing.T) {
	for _, kind := range []string{"offer", "export", "import"} {
		t.Run(kind, func(t *testing.T) {
			source, dest, o, e := fixture(t)
			db, table, call := dest, "workspace_handoff_offers", func(tx *sql.Tx) error { return ReserveOffer(tx, o) }
			if kind == "export" {
				db, table, call = source, "workspace_handoff_exports", func(tx *sql.Tx) error { return StartExport(tx, e) }
			}
			if kind == "import" {
				must(t, transaction(dest, func(tx *sql.Tx) error { return ReserveOffer(tx, o) }))
				table, call = "workspace_handoff_imports", func(tx *sql.Tx) error { return AcceptImport(tx, e.Artifact) }
			}
			_, err := db.Exec(`CREATE TRIGGER drop_admission BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(IGNORE); END`)
			must(t, err)
			if transaction(db, call) == nil {
				t.Fatal("ignored durable write acknowledged")
			}
		})
	}
}

func TestKnownOfferReplayRemainsAvailableAtCapacity(t *testing.T) {
	_, db, o, _ := fixture(t)
	must(t, transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, o) }))
	must(t, transaction(db, func(tx *sql.Tx) error {
		insert, err := tx.Prepare(`INSERT INTO workspace_handoff_offers(creation_id,workspace_id,key,body) VALUES(?,?,?,?)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		for n := 1; n < MaxRecords; n++ {
			copy := o
			copy.Destination.CreationID = fmt.Sprintf("capacity-%d", n)
			copy.Destination.WorkspaceID = fmt.Sprintf("%032x", n)
			copy.Destination.Key = copy.Destination.CreationID
			raw, err := json.Marshal(copy)
			if err != nil {
				return err
			}
			if _, err = insert.Exec(copy.Destination.CreationID, copy.Destination.WorkspaceID, copy.Destination.Key, raw); err != nil {
				return err
			}
		}
		return nil
	}))
	must(t, transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, o) }))
	newOffer := o
	newOffer.Destination.CreationID = "over-budget"
	newOffer.Destination.Key = "over-budget"
	newOffer.Destination.WorkspaceID = strings.Repeat("e", 32)
	if transaction(db, func(tx *sql.Tx) error { return ReserveOffer(tx, newOffer) }) == nil {
		t.Fatal("new offer exceeded journal capacity")
	}
}
