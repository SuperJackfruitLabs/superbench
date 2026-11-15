package history

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func interruptedFixture(t *testing.T) (*sql.DB, Record, InterruptedObservation, *reap.Entry) {
	t.Helper()
	db, err := statestore.Open(filepath.Join(t.TempDir(), "history.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := Record{ID: strings.Repeat("1", 32), Origin: "supervised", Key: "card", Holder: "worker", StartedAt: time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)}
	transaction(t, db, func(tx *sql.Tx) error {
		if err := Schema(tx); err != nil {
			return err
		}
		return Start(tx, r)
	})
	digest, _ := Digest(r)
	o := InterruptedObservation{ApprovalDigest: strings.Repeat("2", 64), PriorDigest: digest, LogState: "present", ObservedAt: r.StartedAt.Add(time.Minute)}
	f := &reap.Entry{Path: "run-" + r.ID + ".log", Mode: uint32(os.FileMode(0600)), Device: 3, Inode: 4, SHA256: strings.Repeat("3", 64)}
	return db, r, o, f
}

func transaction(t *testing.T, db *sql.DB, fn func(*sql.Tx) error) {
	t.Helper()
	if err := attempt(db, fn); err != nil {
		t.Fatal(err)
	}
}

func attempt(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func TestInterruptedObservationPreservesSupervisedProvenanceAndUnknownOutcome(t *testing.T) {
	for _, state := range []string{"present", "absent"} {
		t.Run(state, func(t *testing.T) {
			db, before, o, file := interruptedFixture(t)
			size := int64(17)
			if state == "absent" {
				o.LogState = state
				file = nil
				size = 0
			}
			transaction(t, db, func(tx *sql.Tx) error {
				got, err := ObserveInterrupted(tx, before.ID, o.PriorDigest, o, file, size)
				if err != nil {
					return err
				}
				if got.Origin != before.Origin || got.Holder != before.Holder || got.Key != before.Key || !got.StartedAt.Equal(before.StartedAt) || got.ExitCode != nil || got.Truncated || got.Interruption == nil || got.Revision != 1 || !got.ReleasedAt.IsZero() || !reflect.DeepEqual(got.File, file) {
					t.Fatal("observation fabricated outcome or changed provenance", got)
				}
				return nil
			})
			transaction(t, db, func(tx *sql.Tx) error { return Released(tx, before.ID, strings.Repeat("4", 40)) })
			first, err := Get(db, before.ID)
			if err != nil {
				t.Fatal(err)
			}
			transaction(t, db, func(tx *sql.Tx) error {
				replayed, err := ObserveInterrupted(tx, before.ID, o.PriorDigest, o, file, size)
				if err == nil && !reflect.DeepEqual(replayed, first) {
					t.Fatal("approval replay rewrote released evidence")
				}
				return err
			})
			if err := attempt(db, func(tx *sql.Tx) error {
				return Seal(tx, before.ID, reap.Entry{Path: "run-" + before.ID + ".log", SHA256: strings.Repeat("5", 64)}, 0, nil, false)
			}); err == nil {
				t.Fatal("ordinary seal converted interrupted observation")
			}
		})
	}
}

func TestInterruptedObservationRejectsChangedApprovalAndEvidence(t *testing.T) {
	db, r, o, file := interruptedFixture(t)
	transaction(t, db, func(tx *sql.Tx) error { _, err := ObserveInterrupted(tx, r.ID, o.PriorDigest, o, file, 17); return err })
	changes := map[string]func(*InterruptedObservation, **reap.Entry, *int64){
		"approval": func(o *InterruptedObservation, _ **reap.Entry, _ *int64) { o.ApprovalDigest = strings.Repeat("a", 64) },
		"prior":    func(o *InterruptedObservation, _ **reap.Entry, _ *int64) { o.PriorDigest = strings.Repeat("a", 64) },
		"time": func(o *InterruptedObservation, _ **reap.Entry, _ *int64) {
			o.ObservedAt = o.ObservedAt.Add(time.Second)
		},
		"state":  func(o *InterruptedObservation, _ **reap.Entry, _ *int64) { o.LogState = "absent" },
		"file":   func(_ *InterruptedObservation, f **reap.Entry, _ *int64) { (*f).Inode++ },
		"absent": func(_ *InterruptedObservation, f **reap.Entry, _ *int64) { *f = nil },
		"size":   func(_ *InterruptedObservation, _ **reap.Entry, n *int64) { (*n)++ },
	}
	before, _ := Get(db, r.ID)
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			changed, entry, size := o, *file, int64(17)
			ptr := &entry
			change(&changed, &ptr, &size)
			if err := attempt(db, func(tx *sql.Tx) error {
				_, err := ObserveInterrupted(tx, r.ID, changed.PriorDigest, changed, ptr, size)
				return err
			}); err == nil {
				t.Fatal("changed approval/evidence replay accepted")
			}
			if after, _ := Get(db, r.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed durable observation")
			}
		})
	}
}

func TestInterruptedObservationRejectsInvalidOrNonIntentHistory(t *testing.T) {
	changes := map[string]func(*Record, *InterruptedObservation, **reap.Entry, *int64){
		"stale":         func(r *Record, _ *InterruptedObservation, _ **reap.Entry, _ *int64) { r.Revision++ },
		"released":      func(r *Record, _ *InterruptedObservation, _ **reap.Entry, _ *int64) { r.ReleasedAt = r.StartedAt },
		"known-outcome": func(r *Record, _ *InterruptedObservation, _ **reap.Entry, _ *int64) { x := 0; r.ExitCode = &x },
		"sealed":        func(r *Record, _ *InterruptedObservation, _ **reap.Entry, _ *int64) { r.SealedAt = r.StartedAt },
		"adopted": func(r *Record, _ *InterruptedObservation, _ **reap.Entry, _ *int64) {
			r.Origin = "operator-adopted"
			r.ObservedAt = r.StartedAt
			r.AdoptionDigest = strings.Repeat("f", 64)
		},
		"uppercase": func(_ *Record, o *InterruptedObservation, _ **reap.Entry, _ *int64) {
			o.ApprovalDigest = strings.Repeat("A", 64)
		},
		"nonutc": func(_ *Record, o *InterruptedObservation, _ **reap.Entry, _ *int64) {
			o.ObservedAt = o.ObservedAt.In(time.FixedZone("other", 3600))
		},
		"before-start": func(r *Record, o *InterruptedObservation, _ **reap.Entry, _ *int64) {
			o.ObservedAt = r.StartedAt.Add(-time.Second)
		},
		"null-file": func(_ *Record, _ *InterruptedObservation, f **reap.Entry, _ *int64) { *f = nil },
		"symlink": func(_ *Record, _ *InterruptedObservation, f **reap.Entry, _ *int64) {
			(*f).Mode = uint32(os.ModeSymlink | 0600)
		},
		"public":           func(_ *Record, _ *InterruptedObservation, f **reap.Entry, _ *int64) { (*f).Mode = 0644 },
		"wrong-path":       func(_ *Record, _ *InterruptedObservation, f **reap.Entry, _ *int64) { (*f).Path = "other" },
		"negative-size":    func(_ *Record, _ *InterruptedObservation, _ **reap.Entry, n *int64) { *n = -1 },
		"absent-with-file": func(_ *Record, o *InterruptedObservation, _ **reap.Entry, _ *int64) { o.LogState = "absent" },
		"absent-with-size": func(_ *Record, o *InterruptedObservation, f **reap.Entry, _ *int64) { o.LogState = "absent"; *f = nil },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			db, r, o, file := interruptedFixture(t)
			size := int64(17)
			change(&r, &o, &file, &size)
			transaction(t, db, func(tx *sql.Tx) error { return Save(tx, r) })
			before, _ := Get(db, r.ID)
			if err := attempt(db, func(tx *sql.Tx) error {
				_, err := ObserveInterrupted(tx, r.ID, o.PriorDigest, o, file, size)
				return err
			}); err == nil {
				t.Fatal("invalid observation accepted")
			}
			if after, _ := Get(db, r.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed history")
			}
		})
	}
}

func TestInterruptedEvidenceCannotBeStrippedOrReboundBySave(t *testing.T) {
	db, r, o, file := interruptedFixture(t)
	transaction(t, db, func(tx *sql.Tx) error { _, err := ObserveInterrupted(tx, r.ID, o.PriorDigest, o, file, 17); return err })
	changes := map[string]func(*Record){
		"strip": func(r *Record) { r.Interruption = nil },
		"origin": func(r *Record) {
			r.Origin = "operator-adopted"
			r.ObservedAt = o.ObservedAt
			r.AdoptionDigest = o.ApprovalDigest
		},
		"holder":   func(r *Record) { r.Holder = "other" },
		"start":    func(r *Record) { r.StartedAt = r.StartedAt.Add(-time.Hour) },
		"file":     func(r *Record) { r.File.Inode++ },
		"approval": func(r *Record) { r.Interruption.ApprovalDigest = strings.Repeat("b", 64) },
		"outcome":  func(r *Record) { x := 0; r.ExitCode = &x },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			current, err := Get(db, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			change(&current)
			if err := attempt(db, func(tx *sql.Tx) error { return Save(tx, current) }); err == nil {
				t.Fatal("immutable interrupted evidence changed")
			}
		})
	}
}

func TestLegacyRecordBytesAndDigestsRemainUnchanged(t *testing.T) {
	_, r, _, _ := interruptedFixture(t)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Interruption") {
		t.Fatal("legacy serialization changed")
	}
	want := `{"ID":"11111111111111111111111111111111","Origin":"supervised","ObservedAt":"0001-01-01T00:00:00Z","AdoptionDigest":"","Key":"card","Holder":"worker","StartedAt":"2026-11-15T01:00:00Z","ReleasedAt":"0001-01-01T00:00:00Z","SealedAt":"0001-01-01T00:00:00Z","Checkpoint":"","ExitCode":null,"Truncated":false,"File":null,"Size":0,"Collection":"","CollectedAt":"0001-01-01T00:00:00Z","CollectedSHA256":"","CollectionReason":"","Revision":0}`
	if string(raw) != want {
		t.Fatal("legacy record bytes changed", string(raw))
	}
	sum := sha256.Sum256([]byte(want))
	digest, err := Digest(r)
	if err != nil || digest != hex.EncodeToString(sum[:]) {
		t.Fatal("legacy digest changed", digest, err)
	}
}

func TestInterruptedObservationRollbackRetainsOriginalIntentForExactRetry(t *testing.T) {
	db, before, observation, file := interruptedFixture(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveInterrupted(tx, before.ID, observation.PriorDigest, observation, file, 17); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	current, err := Get(db, before.ID)
	if err != nil || !reflect.DeepEqual(current, before) {
		t.Fatal("rolled-back observation changed original intent", err)
	}
	transaction(t, db, func(tx *sql.Tx) error {
		_, err := ObserveInterrupted(tx, before.ID, observation.PriorDigest, observation, file, 17)
		return err
	})
}
