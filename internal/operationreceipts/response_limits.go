package operationreceipts

import (
	"database/sql"
	"strconv"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
)

// legacyResponseLimit is every identity's retained-response bound unless a
// finite claim recorded a larger one. It equals receiptReservation.
const legacyResponseLimit = receiptReservation
const finiteSchemaVersion = 7

// Format 7 adds only response_limits. It is created by the first finite claim,
// never by Open, so a ledger with the finite profile disabled stays at format 6
// and the previous binary can still open it. Once created the previous binary
// refuses the ledger (unsupported schema) instead of misreading larger bodies.
const responseLimitsTable = `CREATE TABLE response_limits(owner INTEGER NOT NULL,id TEXT NOT NULL,bytes INTEGER NOT NULL CHECK(typeof(bytes)='integer' AND bytes>65536 AND bytes<=2097152),PRIMARY KEY(owner,id));PRAGMA user_version=7`

func validResponseLimit(limit int64) bool {
	return limit == legacyResponseLimit || limit > legacyResponseLimit && limit <= finiteio.MaxBody
}

// finiteFormat binds the observed schema version to the table's presence. A
// partial future (table without version, or version without table) refuses.
func finiteFormat(db *sql.DB, version int) (bool, error) {
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='response_limits'`).Scan(&tables); err != nil {
		return false, err
	}
	if (version == finiteSchemaVersion) != (tables == 1) {
		return false, errInDoubt
	}
	return tables == 1, nil
}

// upgradeFiniteTx upgrades exactly format 6 inside the caller's claim
// transaction, so a refused or failed first claim rolls the upgrade back too.
func upgradeFiniteTx(tx *sql.Tx) error {
	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != 6 {
		return errInDoubt
	}
	_, err := tx.Exec(responseLimitsTable)
	return err
}

// responseLimit is the identity's retained bound. Absence means legacy; a
// malformed row never relaxes or tightens a bound silently.
func (r *receipts) responseLimit(q cancelReader, uid uint32, id string) (int64, error) {
	if !r.finite {
		return legacyResponseLimit, nil
	}
	var limit int64
	err := q.QueryRow(`SELECT bytes FROM response_limits WHERE owner=? AND id=?`, uid, id).Scan(&limit)
	if err == sql.ErrNoRows {
		return legacyResponseLimit, nil
	}
	if err != nil {
		return 0, err
	}
	if limit == legacyResponseLimit || !validResponseLimit(limit) {
		return 0, errInDoubt
	}
	return limit, nil
}

// reserveResponse charges the whole bound before any effect, in the claim
// transaction and before its identity is inserted. Legacy claims in format 6
// keep the original count arithmetic; recordResponseLimit then binds the row.
// It reports whether this transaction created format 7; the caller marks the
// ledger finite only after its commit.
func (r *receipts) reserveResponse(tx *sql.Tx, limit int64) (upgraded bool, err error) {
	if !validResponseLimit(limit) {
		return false, errInDoubt
	}
	if limit != legacyResponseLimit && !r.finite {
		if err = upgradeFiniteTx(tx); err != nil {
			return false, err
		}
		upgraded = true
	}
	if !r.finite && !upgraded {
		return false, requireReceiptCapacity(tx)
	}
	var reserved int64
	if err = tx.QueryRow(`SELECT COALESCE(SUM(COALESCE(l.bytes,` + strconv.FormatInt(legacyResponseLimit, 10) + `)),0) FROM (` + unresolvedIdentities + `) u LEFT JOIN response_limits l ON l.owner=u.owner AND l.id=u.id`).Scan(&reserved); err != nil {
		return false, err
	}
	if reserved < 0 || reserved+limit > activeReceiptBytes {
		return false, errCapacity
	}
	return upgraded, nil
}
func (r *receipts) recordResponseLimit(tx *sql.Tx, uid uint32, id string, limit int64) error {
	if limit == legacyResponseLimit {
		return nil
	}
	result, err := tx.Exec(`INSERT INTO response_limits(owner,id,bytes) VALUES(?,?,?)`, uid, id, limit)
	return oneInserted(result, err)
}

// validateResponseLimits rejects orphaned or out-of-range limit rows at open.
func (r *receipts) validateResponseLimits() error {
	if !r.finite {
		return nil
	}
	var invalid int
	if err := r.db.QueryRow(`SELECT count(*) FROM response_limits l LEFT JOIN receipts r ON r.owner=l.owner AND r.id=l.id WHERE r.id IS NULL OR typeof(l.bytes)<>'integer' OR l.bytes<=65536 OR l.bytes>2097152`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errInDoubt
	}
	return nil
}

// historyBodies selects retained bodies within each identity's own bound;
// a larger body reads as NULL and so fails validation instead of loading.
func (r *receipts) historyBodies() string {
	limit := `65536`
	join := ``
	if r.finite {
		limit = `COALESCE(l.bytes,65536)`
		join = ` LEFT JOIN response_limits l ON l.owner=receipts.owner AND l.id=receipts.id`
	}
	return `SELECT receipts.owner,receipts.id,digest,state,cancel_state,CASE WHEN length(body)<=` + limit + ` THEN body ELSE NULL END,status FROM receipts` + join + ` WHERE receipts.owner>? OR (receipts.owner=? AND receipts.id>?) ORDER BY receipts.owner,receipts.id LIMIT 64`
}
