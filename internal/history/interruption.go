package history

import (
	"database/sql"
	"errors"
	"os"
	"reflect"
	"regexp"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

var canonicalDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

// InterruptedObservation records a reviewed post-fence observation. Neither a
// present file nor an absent file establishes the original capture's completeness,
// truncation status or command outcome. Original supervised provenance remains.
type InterruptedObservation struct {
	ApprovalDigest string
	PriorDigest    string
	LogState       string // present or absent
	ObservedAt     time.Time
}

func validInterruption(r Record) error {
	o := r.Interruption
	if o == nil {
		return nil
	}
	_, offset := o.ObservedAt.Zone()
	if r.Origin != "supervised" || !canonicalDigest.MatchString(o.ApprovalDigest) || !canonicalDigest.MatchString(o.PriorDigest) || o.ObservedAt.IsZero() || offset != 0 || o.ObservedAt.Before(r.StartedAt) || !r.SealedAt.Equal(o.ObservedAt) || r.ExitCode != nil || r.Truncated || r.Revision < 1 {
		return errors.New("invalid interrupted supervision observation")
	}
	switch o.LogState {
	case "present":
		if r.File == nil || r.File.Path != "run-"+r.ID+".log" || !os.FileMode(r.File.Mode).IsRegular() || os.FileMode(r.File.Mode).Perm()&0077 != 0 || r.File.Inode == 0 || !canonicalDigest.MatchString(r.File.SHA256) {
			return errors.New("invalid interrupted log snapshot")
		}
	case "absent":
		if r.File != nil || r.Size != 0 || r.Collection != "" || !r.CollectedAt.IsZero() {
			return errors.New("absent interrupted capture cannot acquire file evidence")
		}
	default:
		return errors.New("unknown interrupted log observation state")
	}
	return nil
}

// ObserveInterrupted applies a caller-verified, digest-bound administrative
// observation to existing unsealed supervised history. The caller owns fencing,
// scope ownership, file locking and exact snapshot/absence verification. This
// function never releases ownership or invents an execution outcome.
func ObserveInterrupted(tx *sql.Tx, id, expectedDigest string, observation InterruptedObservation, file *reap.Entry, size int64) (Record, error) {
	r, err := Get(tx, id)
	if err != nil {
		return Record{}, err
	}
	if expectedDigest != observation.PriorDigest || !canonicalDigest.MatchString(expectedDigest) {
		return Record{}, errors.New("interrupted observation prior digest mismatch")
	}
	if r.Interruption != nil {
		if sameObservation(r.Interruption, &observation) && reflect.DeepEqual(r.File, file) && r.Size == size {
			return r, nil
		}
		return Record{}, errors.New("interrupted observation approval changed")
	}
	if r.Origin != "supervised" || !r.ReleasedAt.IsZero() || !r.SealedAt.IsZero() || r.File != nil || r.ExitCode != nil || r.Truncated || r.Size != 0 || r.Collection != "" || !r.CollectedAt.IsZero() {
		return Record{}, errors.New("interrupted observation requires held unsealed supervised history")
	}
	prior, err := Digest(r)
	if err != nil {
		return Record{}, err
	}
	if prior != expectedDigest {
		return Record{}, errors.New("history changed after interruption approval")
	}
	copy := observation
	r.Interruption = &copy
	if file != nil {
		entry := *file
		r.File = &entry
	}
	r.Size, r.SealedAt = size, observation.ObservedAt
	r.Revision++
	if err := save(tx, r, true); err != nil {
		return Record{}, err
	}
	return r, nil
}

func sameObservation(a, b *InterruptedObservation) bool {
	return a != nil && b != nil && a.ApprovalDigest == b.ApprovalDigest && a.PriorDigest == b.PriorDigest && a.LogState == b.LogState && a.ObservedAt.Equal(b.ObservedAt)
}

func sameInterruptedEvidence(a, b Record) bool {
	return a.ID == b.ID && a.Origin == b.Origin && a.Key == b.Key && a.Holder == b.Holder && a.StartedAt.Equal(b.StartedAt) && a.ObservedAt.Equal(b.ObservedAt) && a.AdoptionDigest == b.AdoptionDigest && reflect.DeepEqual(a.File, b.File) && a.Size == b.Size && a.SealedAt.Equal(b.SealedAt) && b.ExitCode == nil && !b.Truncated && b.Revision >= a.Revision
}
