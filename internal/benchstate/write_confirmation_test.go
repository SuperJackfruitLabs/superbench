package benchstate

import (
	"database/sql"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestIgnoredCoreWriteCannotAcknowledgeTransition(t *testing.T) {
	db := oldDatabase(t, 27, aggregate())
	if err := migrateForTest(db, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER ignore_new_record BEFORE INSERT ON benches WHEN NEW.key='newrow' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = Update(tx, func(rows Database, _ *sql.Tx) error {
		rows["newrow"] = &model.Bench{RegistrationID: "0123456789abcdef0123456789abcdef", Key: "newrow", Path: "/fixed/benches/newrow", Mirror: "/fixed/mirrors/repo", Repo: "repo", Branch: "card/newrow"}
		return nil
	})
	if err == nil {
		t.Fatal("ignored core write acknowledged")
	}
}
