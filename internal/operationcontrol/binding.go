// Package operationcontrol binds a transport operation to one private writer
// epoch. Its records are authority, never a timer or permission to resume work.
package operationcontrol

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

var (
	ErrAuthority = errors.New("operation writer authority changed")
	ErrBusy      = errors.New("workspace has an active operation")
	ErrConflict  = errors.New("operation identity conflicts with retained state")
)

const MaxOperations = 10000

var key = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var token = regexp.MustCompile(`^([a-f0-9]{32}|[a-f0-9]{48})$`)

type Target struct {
	UID               uint32
	RequestID, Digest string
}

func (t Target) Valid() bool { return key.MatchString(t.RequestID) && hex64.MatchString(t.Digest) }
func (t Target) ID() string {
	raw, _ := json.Marshal(struct {
		Domain string
		Target Target
	}{"superbench.operation.v1", t})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type Intent struct {
	Target                Target
	Key, Operation, Epoch string
}

func (i Intent) Valid() bool {
	if !i.Target.Valid() || !key.MatchString(i.Key) || !hex32.MatchString(i.Epoch) {
		return false
	}
	switch i.Operation {
	case "create", "recover", "lease", "setup", "run", "initialization-repair":
		return true
	}
	return false
}

type Binding struct {
	Control                                             Control
	Intent                                              Intent
	BenchID, Holder, TokenHash, ContinuationHash, Phase string
}
type Reader interface{ QueryRow(string, ...any) *sql.Row }

func Schema(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS workspace_operations (
 id TEXT PRIMARY KEY,owner INTEGER NOT NULL,request_id TEXT NOT NULL,digest TEXT NOT NULL,
 key TEXT NOT NULL,operation TEXT NOT NULL,epoch TEXT NOT NULL,bench_id TEXT NOT NULL,
 holder TEXT NOT NULL,token_hash TEXT NOT NULL,continuation_hash TEXT NOT NULL,
 phase TEXT NOT NULL CHECK(phase IN ('admitted','bound','stopped','finished')),
 control_state TEXT NOT NULL DEFAULT '',control_root TEXT NOT NULL DEFAULT '',control_host TEXT NOT NULL DEFAULT '',control_boot TEXT NOT NULL DEFAULT '',
 UNIQUE(owner,request_id));
 CREATE UNIQUE INDEX IF NOT EXISTS workspace_operation_active ON workspace_operations(key) WHERE phase IN ('admitted','bound')`)
	if err != nil {
		return err
	}
	if err = runSchema(tx); err != nil {
		return err
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS workspace_operation_cancellations(operation_id TEXT PRIMARY KEY REFERENCES workspace_operations(id),record TEXT NOT NULL CHECK(json_valid(record)))`)
	return err
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func validBinding(b Binding) bool {
	if !b.Intent.Valid() || !b.Control.Valid() {
		return false
	}
	if b.BenchID != "" && !hex32.MatchString(b.BenchID) {
		return false
	}
	switch b.Phase {
	case "admitted":
		return b.Holder == "" && b.TokenHash == "" && b.ContinuationHash == ""
	case "bound", "stopped":
		return hex32.MatchString(b.BenchID) && b.Holder != "" && len(b.Holder) <= 200 && hex64.MatchString(b.TokenHash) && hex64.MatchString(b.ContinuationHash)
	case "finished":
		return (b.Holder == "" && b.TokenHash == "" && b.ContinuationHash == "") || (hex32.MatchString(b.BenchID) && b.Holder != "" && hex64.MatchString(b.TokenHash) && hex64.MatchString(b.ContinuationHash))
	}
	return false
}

type scanner interface{ Scan(...any) error }

func scan(row scanner) (Binding, error) { return scanBinding(row, true) }
func scanBinding(row scanner, withControl bool) (Binding, error) {
	var b Binding
	var uid int64
	var storedID string
	fields := []any{&storedID, &uid, &b.Intent.Target.RequestID, &b.Intent.Target.Digest, &b.Intent.Key, &b.Intent.Operation, &b.Intent.Epoch, &b.BenchID, &b.Holder, &b.TokenHash, &b.ContinuationHash, &b.Phase}
	if withControl {
		fields = append(fields, &b.Control.State, &b.Control.Root, &b.Control.HostID, &b.Control.BootID)
	}
	err := row.Scan(fields...)
	if err != nil {
		return b, err
	}
	if uid < 0 || uid > 1<<32-1 {
		return b, ErrAuthority
	}
	b.Intent.Target.UID = uint32(uid)
	if !validBinding(b) || storedID != b.Intent.Target.ID() {
		return b, ErrAuthority
	}
	return b, nil
}

const legacyColumns = `owner,request_id,digest,key,operation,epoch,bench_id,holder,token_hash,continuation_hash,phase`
const columns = legacyColumns + `,control_state,control_root,control_host,control_boot`

func Get(r Reader, t Target) (Binding, error) {
	if !t.Valid() {
		return Binding{}, ErrConflict
	}
	b, err := scan(r.QueryRow(`SELECT id,`+columns+` FROM workspace_operations WHERE owner=? AND request_id=?`, t.UID, t.RequestID))
	if err != nil {
		return b, err
	}
	if b.Intent.Target != t {
		return b, ErrConflict
	}
	return b, nil
}

// Begin must commit before any planning Git, allocation or filesystem effects.
// It deliberately permits a fresh creation to have no selected BID/lease yet.
func Begin(tx *sql.Tx, i Intent, benchID string) error {
	return BeginWithControl(tx, i, benchID, Control{})
}
func BeginWithControl(tx *sql.Tx, i Intent, benchID string, c Control) error {
	if !c.Valid() || !i.Valid() || benchID != "" && !hex32.MatchString(benchID) {
		return ErrConflict
	}
	old, err := Get(tx, i.Target)
	if err == nil {
		if old.Intent != i || old.BenchID != benchID || old.Phase != "admitted" || old.Control != c {
			return ErrConflict
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM workspace_operations WHERE key=? AND phase IN ('admitted','bound')`, i.Key).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrBusy
	}
	if err = tx.QueryRow(`SELECT count(*) FROM workspace_operations`).Scan(&count); err != nil {
		return err
	}
	if count >= MaxOperations {
		return errors.New("operation binding capacity exhausted")
	}
	result, err := tx.Exec(`INSERT INTO workspace_operations (`+`id,`+columns+`) VALUES (?,?,?,?,?,?,?,?,'','','','admitted',?,?,?,?)`, i.Target.ID(), i.Target.UID, i.Target.RequestID, i.Target.Digest, i.Key, i.Operation, i.Epoch, benchID, c.State, c.Root, c.HostID, c.BootID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAuthority
	}
	return nil
}

// Bind is called in the SAME SQL transaction that saves this exact lease.
// There is no window in which a newly saved lease lacks its operation epoch.
func Bind(tx *sql.Tx, i Intent, b *model.Bench) error {
	old, err := Get(tx, i.Target)
	if err != nil {
		return err
	}
	if old.Intent != i || old.Phase != "admitted" || b == nil || b.Workspace == nil || b.Lease == nil || b.Key != i.Key || !hex32.MatchString(b.Workspace.ID) || old.BenchID != "" && old.BenchID != b.Workspace.ID || !token.MatchString(b.Lease.Token) || !hex32.MatchString(b.Lease.Continuation) || b.Lease.Holder == "" || b.Lease.StopRequested {
		return ErrAuthority
	}
	result, err := tx.Exec(`UPDATE workspace_operations SET bench_id=?,holder=?,token_hash=?,continuation_hash=?,phase='bound' WHERE id=? AND epoch=? AND phase='admitted'`, b.Workspace.ID, b.Lease.Holder, hash(b.Lease.Token), hash(b.Lease.Continuation), i.Target.ID(), i.Epoch)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAuthority
	}
	return nil
}
func matches(b Binding, i Intent, v *model.Bench) bool {
	return b.Intent == i && b.Phase == "bound" && v != nil && v.Workspace != nil && v.Lease != nil && v.Key == i.Key && v.Workspace.ID == b.BenchID && v.Lease.Holder == b.Holder && hash(v.Lease.Token) == b.TokenHash && hash(v.Lease.Continuation) == b.ContinuationHash && !v.Lease.StopRequested
}
func Require(r Reader, i Intent, b *model.Bench) error {
	found, err := Get(r, i.Target)
	if err != nil {
		return err
	}
	if !matches(found, i, b) {
		return ErrAuthority
	}
	return nil
}

// RequireContext protects a bound operation even from a local caller holding
// the same token. A future request cannot inherit an earlier request's epoch.
func RequireContext(r Reader, b *model.Bench, i *Intent) error {
	if b == nil {
		return ErrAuthority
	}
	active, err := scan(r.QueryRow(`SELECT id,`+columns+` FROM workspace_operations WHERE key=? AND phase IN ('admitted','bound')`, b.Key))
	if err == sql.ErrNoRows {
		if i == nil {
			return nil
		}
		return ErrAuthority
	}
	if err != nil {
		return err
	}
	if i == nil {
		return ErrBusy
	}
	if !matches(active, *i, b) {
		return ErrAuthority
	}
	return nil
}
