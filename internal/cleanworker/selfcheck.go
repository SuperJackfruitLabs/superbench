package cleanworker

import (
	"fmt"
	"strconv"
	"strings"
)

// Identity is the daemon uid and gid root dropped the worker to. Root passes
// it as the worker's two arguments; the worker proves it holds exactly that.
type Identity struct{ UID, GID uint32 }

// Arguments is the worker's argv after the command: the decimal uid and gid.
func Arguments(i Identity) []string {
	return []string{strconv.FormatUint(uint64(i.UID), 10), strconv.FormatUint(uint64(i.GID), 10)}
}

// parseArgs accepts exactly two canonical nonzero decimal IDs.
func parseArgs(args []string) (Identity, error) {
	if len(args) != 2 {
		return Identity{}, fmt.Errorf("%w: arguments", ErrWorker)
	}
	var ids [2]uint32
	for k, s := range args {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil || v == 0 || strconv.FormatUint(v, 10) != s {
			return Identity{}, fmt.Errorf("%w: arguments", ErrWorker)
		}
		ids[k] = uint32(v)
	}
	return Identity{ids[0], ids[1]}, nil
}

// statusKeys are checked in this order, so a refusal names its first cause.
var statusKeys = []string{"Uid:", "Gid:", "Groups:", "CapInh:", "CapPrm:", "CapEff:", "CapBnd:", "CapAmb:", "NoNewPrivs:"}

// verifyStatus requires /proc/self/status to show exactly the daemon uid and
// gid in every slot, no supplementary groups, every capability set empty and
// no_new_privs, each line exactly once, as payloadexec's verifyDropped does.
func verifyStatus(raw []byte, want Identity) error {
	if len(raw) > 64<<10 {
		return fmt.Errorf("%w: status", ErrWorker)
	}
	u, g := strconv.FormatUint(uint64(want.UID), 10), strconv.FormatUint(uint64(want.GID), 10)
	zero := "0000000000000000"
	wanted := map[string]string{"Uid:": strings.Join([]string{u, u, u, u}, " "), "Gid:": strings.Join([]string{g, g, g, g}, " "), "Groups:": "",
		"CapInh:": zero, "CapPrm:": zero, "CapEff:": zero, "CapBnd:": zero, "CapAmb:": zero, "NoNewPrivs:": "1"}
	seen := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, _ := strings.Cut(line, "\t")
		if _, check := wanted[key]; !check {
			continue
		}
		if _, again := seen[key]; again {
			return fmt.Errorf("%w: duplicate %s", ErrWorker, key)
		}
		seen[key] = strings.Join(strings.Fields(value), " ")
	}
	for _, key := range statusKeys {
		if got, ok := seen[key]; !ok || got != wanted[key] {
			return fmt.Errorf("%w: %s", ErrWorker, strings.TrimSuffix(key, ":"))
		}
	}
	return nil
}

// verifyDescriptors requires the /proc/self/fd listing, less the descriptor
// that read it, to be exactly 0, 1 and 2.
func verifyDescriptors(names []string, own int) error {
	found := map[int]bool{}
	for _, name := range names {
		if name == "." || name == ".." {
			continue
		}
		fd, err := strconv.Atoi(name)
		if err != nil || strconv.Itoa(fd) != name || fd < 0 {
			return fmt.Errorf("%w: descriptors", ErrWorker)
		}
		if fd == own {
			continue
		}
		if fd > 2 || found[fd] {
			return fmt.Errorf("%w: descriptors: unexpected %d", ErrWorker, fd)
		}
		found[fd] = true
	}
	if len(found) != 3 {
		return fmt.Errorf("%w: descriptors", ErrWorker)
	}
	return nil
}
