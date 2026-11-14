// Package archivepolicy validates persisted archive retention boundaries.
package archivepolicy

import (
	"encoding/hex"
	"errors"
	"fmt"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"time"
)

func ValidateArchive(b *model.Bench) error {
	if e := Validate(b.ArchivedAt, b.ReapAfter); e != nil {
		return e
	}
	if b.ArchiveFreezeDigest != "" {
		if decoded, e := hex.DecodeString(b.ArchiveFreezeDigest); e != nil || len(decoded) != 32 || b.ArchivedAt == "" {
			return errors.New("invalid archive permission digest")
		}
	}
	if b.ArchiveFrozenAt != "" {
		at, e := time.Parse(time.RFC3339Nano, b.ArchiveFrozenAt)
		start, _ := time.Parse(time.RFC3339Nano, b.ArchivedAt)
		if e != nil || at.IsZero() || at.Before(start) || b.ArchiveFreezeDigest == "" {
			return errors.New("invalid archive permission completion")
		}
	}
	if b.ReapingDigest != "" && b.ArchiveFreezeDigest != "" && b.ArchiveFrozenAt == "" {
		return errors.New("deletion intent precedes archive permission completion")
	}
	return nil
}

const DefaultGrace = 7 * 24 * time.Hour
const MaxGrace = 365 * 24 * time.Hour

func Duration(grace time.Duration) error {
	if grace < 0 || grace > MaxGrace {
		return errors.New("archive grace must be between zero and 365 days")
	}
	return nil
}

func Deadline(archived string, grace time.Duration) (string, error) {
	if e := Duration(grace); e != nil {
		return "", e
	}
	at, e := time.Parse(time.RFC3339Nano, archived)
	if e != nil || at.IsZero() {
		return "", errors.New("invalid archive timestamp")
	}
	return at.Add(grace).UTC().Format(time.RFC3339Nano), nil
}

// Validate allows an absent deadline only so legacy records can be loaded for
// migration. Eligible never permits deletion without a persisted deadline.
func Validate(archived, deadline string) error {
	if archived == "" {
		if deadline != "" {
			return errors.New("archive deadline without an archive")
		}
		return nil
	}
	at, e := time.Parse(time.RFC3339Nano, archived)
	if e != nil || at.IsZero() {
		return errors.New("invalid archive timestamp")
	}
	if deadline == "" {
		return nil
	}
	end, e := time.Parse(time.RFC3339Nano, deadline)
	if e != nil || end.Before(at) || end.After(at.Add(MaxGrace)) {
		return errors.New("invalid archive grace deadline")
	}
	return nil
}

func Eligible(archived, deadline string, now time.Time) error {
	if e := Validate(archived, deadline); e != nil {
		return e
	}
	if archived == "" || deadline == "" {
		return errors.New("archive has no persisted grace deadline; reconcile state")
	}
	end, _ := time.Parse(time.RFC3339Nano, deadline)
	if now.Before(end) {
		return fmt.Errorf("archive grace has not elapsed; reap is available after %s", deadline)
	}
	return nil
}
