// Package receiptlimittest exercises operationreceipts' finite response limits
// through its exported ledger API only. It is a test-only package.
package receiptlimittest

import (
	"bytes"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationreceipts"
)

const finiteLimit = 1536 << 10

func digest(n int) string { return strings.Repeat(string(rune('a'+n%6)), 64) }
func body(size int) []byte {
	raw := append([]byte(`"`), bytes.Repeat([]byte("x"), size-2)...)
	return append(raw, '"')
}
func open(t *testing.T, state string) *operationreceipts.Ledger {
	t.Helper()
	ledger, err := operationreceipts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}
func raw(t *testing.T, state string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(state, "service.db")}).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func format(t *testing.T, state string) (int, int) {
	t.Helper()
	var version, tables int
	db := raw(t, state)
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='response_limits'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	return version, tables
}

func TestLegacyClaimsKeepFormatSixAndTheirBound(t *testing.T) {
	state := t.TempDir()
	ledger := open(t, state)
	defer ledger.Close()
	if _, err := ledger.Claim(1, "legacy", digest(0)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Finish(1, "legacy", body(operationreceipts.LegacyResponseLimit+1), 200); !errors.Is(err, operationreceipts.ErrCapacity) {
		t.Fatal("legacy identity retained a larger body", err)
	}
	if _, err := ledger.ClaimLimit(1, "explicit", digest(1), operationreceipts.LegacyResponseLimit); err != nil {
		t.Fatal(err)
	}
	if version, tables := format(t, state); version != 6 || tables != 0 {
		t.Fatal("legacy claims changed the ledger format", version, tables)
	}
	for _, limit := range []int{0, operationreceipts.LegacyResponseLimit - 1, finiteio.MaxBody + 1} {
		if _, err := ledger.ClaimLimit(1, "invalid", digest(2), limit); err == nil {
			t.Fatal("invalid response bound claimed", limit)
		}
	}
	if found, _, _ := lookup(ledger, "invalid", digest(2)); found {
		t.Fatal("invalid bound reserved an identity")
	}
}
func lookup(ledger *operationreceipts.Ledger, id, d string) (bool, []byte, error) {
	r, found, err := ledger.Lookup(1, id, d)
	if r == nil {
		return found, nil, err
	}
	return found, r.Bytes(), err
}

func TestFiniteClaimUpgradesOnceAndRetainsOnlyItsOwnBound(t *testing.T) {
	state := t.TempDir()
	ledger := open(t, state)
	if _, err := ledger.ClaimLimit(1, "finite", digest(0), finiteLimit); err != nil {
		t.Fatal(err)
	}
	if version, tables := format(t, state); version != 7 || tables != 1 {
		t.Fatal("finite claim did not record per-identity limits", version, tables)
	}
	if _, err := ledger.ClaimLimit(1, "over", digest(1), finiteLimit); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Finish(1, "over", body(finiteLimit+1), 200); !errors.Is(err, operationreceipts.ErrCapacity) {
		t.Fatal("finite identity exceeded its own bound", err)
	}
	if _, err := ledger.Claim(1, "legacy", digest(2)); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Finish(1, "legacy", body(operationreceipts.LegacyResponseLimit+1), 200); !errors.Is(err, operationreceipts.ErrCapacity) {
		t.Fatal("finite format widened a legacy identity", err)
	}
	want := body(finiteLimit)
	if err := ledger.Finish(1, "finite", want, 200); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := open(t, state)
	defer reopened.Close()
	found, got, err := lookup(reopened, "finite", digest(0))
	if err != nil || !found || !bytes.Equal(got, want) {
		t.Fatal("finite receipt not replayable after restart", found, len(got), err)
	}
	if found, _, err = lookup(reopened, "over", digest(1)); !found || !errors.Is(err, operationreceipts.ErrInDoubt) {
		t.Fatal("refused finish became replayable or absent", found, err)
	}
}

func TestFiniteReservationIsChargedBeforeInsertion(t *testing.T) {
	state := t.TempDir()
	ledger := open(t, state)
	defer ledger.Close()
	// 64 MiB of unresolved responses: exactly 32 identities of 2 MiB each.
	for i := 0; i < 32; i++ {
		if _, err := ledger.ClaimLimit(1, "big-"+string(rune('a'+i)), digest(i), finiteio.MaxBody); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := ledger.ClaimLimit(1, "big-over", digest(0), finiteio.MaxBody); !errors.Is(err, operationreceipts.ErrCapacity) {
		t.Fatal("finite reservation exceeded the unresolved budget", err)
	}
	if _, err := ledger.Claim(1, "small", digest(1)); !errors.Is(err, operationreceipts.ErrCapacity) {
		t.Fatal("legacy reservation ignored finite reservations", err)
	}
	if found, _, _ := lookup(ledger, "big-over", digest(0)); found {
		t.Fatal("refused reservation retained an identity")
	}
	if err := ledger.Finish(1, "big-a", body(1024), 200); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Claim(1, "small", digest(1)); err != nil {
		t.Fatal("completed finite identity still reserved its bound", err)
	}
}

func TestOpenRefusesPartialOrOrphanedFiniteFormat(t *testing.T) {
	for name, damage := range map[string]string{
		"table-without-version": `CREATE TABLE response_limits(owner INTEGER NOT NULL,id TEXT NOT NULL,bytes INTEGER NOT NULL,PRIMARY KEY(owner,id))`,
		"version-without-table": `PRAGMA user_version=7`,
		"orphaned-limit":        `CREATE TABLE response_limits(owner INTEGER NOT NULL,id TEXT NOT NULL,bytes INTEGER NOT NULL,PRIMARY KEY(owner,id));INSERT INTO response_limits VALUES(1,'missing',100000);PRAGMA user_version=7`,
		"unsupported-future":    `PRAGMA user_version=8`,
	} {
		t.Run(name, func(t *testing.T) {
			state := t.TempDir()
			ledger := open(t, state)
			if err := ledger.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := raw(t, state).Exec(damage); err != nil {
				t.Fatal(err)
			}
			if ledger, err := operationreceipts.Open(state); err == nil {
				_ = ledger.Close()
				t.Fatal("damaged finite format opened")
			}
		})
	}
}
