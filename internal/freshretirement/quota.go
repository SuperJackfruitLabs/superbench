package freshretirement

import (
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/storageproof"
	"strconv"
	"strings"
)

// Missing records are ambiguous, not zero. Zero hard caps may reflect an
// interrupted initial limit setup; changed nonzero caps are never accepted.
func unused(raw []byte, id uint32, hard uint64) error {
	if err := storageproof.VerifyUnused(raw, id); err != nil {
		return errors.Join(ErrRetirement, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 0 && fields[0] == "#"+strconv.FormatUint(uint64(id), 10) {
			if len(fields) < 4 {
				return ErrRetirement
			}
			cap, err := strconv.ParseUint(fields[3], 10, 64)
			soft, softErr := strconv.ParseUint(fields[2], 10, 64)
			if err != nil || softErr != nil || soft != 0 || cap != 0 && cap != hard {
				return ErrRetirement
			}
		}
	}
	return nil
}

type reportOutput struct{ raw []byte }

func (b *reportOutput) Write(raw []byte) (int, error) {
	if len(b.raw)+len(raw) > 64<<10 {
		return 0, ErrRetirement
	}
	b.raw = append(b.raw, raw...)
	return len(raw), nil
}

// ordered is retirement's fixed sequence: the leaf is absent, usage is zero,
// the data filesystem is synced so that removal is durable, the leaf is still
// absent, and only then is the tombstone saved (save is nil when only
// observing) and absence checked once more. A tombstone on the ledger
// filesystem must never outlive an unsynced removal on the data filesystem.
func ordered(absent, unused, sync, save func() error) error {
	steps := []func() error{absent, unused, sync, absent}
	if save != nil {
		steps = append(steps, save, absent)
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// admitted is who may hold a Fresh allocation: root, and not a sudo caller,
// except for the reclaim entry points (helper), whose authority is the
// reclaim operation's reservation that the fixed helper holds.
func admitted(uid, euid int, sudo string, helper bool) bool {
	return uid == 0 && euid == 0 && (sudo == "" || helper)
}
