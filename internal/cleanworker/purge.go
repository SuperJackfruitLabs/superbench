package cleanworker

import (
	"fmt"
	"strconv"
	"strings"
)

// purgeDepth bounds the directories a purge holds open at once. A directory
// found deeper is renamed up beneath the tree's root and emptied from there,
// so a tree of any depth is deleted with at most this many descriptors.
const purgeDepth = 64

// purgeSteps bounds one purge's directory-entry operations. The bench's inode
// quota bounds its entries; the byte quota bounds the names hard links add. A
// purge that stops at the bound has only removed entries, so the same purge
// continues where it stopped.
const purgeSteps = 1 << 26

// liftTries bounds the fresh names one lift tries beneath the root.
const liftTries = 1000

// liftPrefix names a directory a purge renamed up beneath the root.
const liftPrefix = ".superbench-purge-"

func liftName(n uint64) string { return liftPrefix + strconv.FormatUint(n, 10) }

// mountsBeneath refuses a /proc/self/mountinfo listing with a mount at or
// beneath root. A purge never crosses into another mount, even one of the
// same filesystem, which comparing device numbers alone would not show.
func mountsBeneath(mountinfo []byte, root string) error {
	if !validRoot(root) {
		return fmt.Errorf("%w: purge root", ErrWorker)
	}
	for _, line := range strings.Split(string(mountinfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 5 {
			return fmt.Errorf("%w: mountinfo", ErrWorker)
		}
		point, ok := unescapeMount(fields[4])
		if !ok {
			return fmt.Errorf("%w: mountinfo", ErrWorker)
		}
		if point == root || strings.HasPrefix(point, root+"/") {
			return fmt.Errorf("%w: a mount at or beneath the tree", ErrWorker)
		}
	}
	return nil
}

// unescapeMount decodes mountinfo's octal escapes (\040 for a space, \011,
// \012 and \134). Any other backslash is malformed.
func unescapeMount(s string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out.WriteByte(s[i])
			continue
		}
		if i+4 > len(s) {
			return "", false
		}
		v, err := strconv.ParseUint(s[i+1:i+4], 8, 8)
		if err != nil {
			return "", false
		}
		out.WriteByte(byte(v))
		i += 3
	}
	return out.String(), true
}
