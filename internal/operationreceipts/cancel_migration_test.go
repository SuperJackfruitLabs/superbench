package operationreceipts

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLegacyReceiptMigrationPreservesHistoryAndCannotResumeOldRunningWork(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "service.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE receipts(owner INTEGER NOT NULL,id TEXT NOT NULL,digest TEXT NOT NULL,state TEXT NOT NULL,body BLOB,status INTEGER,PRIMARY KEY(owner,id));PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	body := []byte(`{"historical":"unchanged"}`)
	if _, err = db.Exec(`INSERT INTO receipts VALUES(1,'done',?,'done',?,200),(1,'running',?,'running',NULL,NULL)`, digest, body, digest); err != nil {
		t.Fatal(err)
	}
	db.Close()
	r, err := openReceipts(state)
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	var version int
	if err = r.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != receiptSchemaVersion {
		t.Fatal(version, err)
	}
	got, _, err := r.lookup(1, "done", digest)
	if err != nil || got == nil || !bytes.Equal(got.body, body) {
		t.Fatal("legacy history changed", err)
	}
	if _, err = r.claim(1, "running", digest); !errors.Is(err, errInDoubt) {
		t.Fatal("old work resumed", err)
	}
	if state, err := r.requestCancel(1, "running", digest); err != nil || state != "cancel_requested" {
		t.Fatal("interrupted work claimed fenced", state, err)
	}
}
func TestCancelAndFinalReceiptHaveOneDurableWinner(t *testing.T) {
	for n := 0; n < 12; n++ {
		r, err := openReceipts(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		digest := strings.Repeat("b", 64)
		if _, err = r.claim(7, "race", digest); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var state string
		var cancelErr, finishErr error
		go func() { defer wg.Done(); <-start; state, cancelErr = r.requestCancel(7, "race", digest) }()
		go func() {
			defer wg.Done()
			<-start
			finishErr = r.finish(7, "race", []byte(`{"original":"immutable"}`), 200)
		}()
		close(start)
		wg.Wait()
		if cancelErr != nil {
			t.Fatal(cancelErr)
		}
		switch state {
		case "already_completed":
			if finishErr != nil {
				t.Fatal("completion winner inconsistent", finishErr)
			}
		case "cancel_requested":
			if !errors.Is(finishErr, errCancelRequested) {
				t.Fatal("grant crossed cancel barrier", finishErr)
			}
		default:
			t.Fatal(state)
		}
		r.db.Close()
	}
}
