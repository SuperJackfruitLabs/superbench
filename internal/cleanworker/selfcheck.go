package cleanworker

import (
	"fmt"
	"strconv"
	"strings"
)

// Identity is what root dropped the worker to: the daemon uid and gid, and
// the serial of root's own session keyring before root's forking thread joined
// a fresh one for the worker (0 when root had no live session keyring). Root
// passes it as the worker's three arguments; the worker proves it holds
// exactly those IDs and does not hold root's keyring.
type Identity struct {
	UID, GID uint32
	Keyring  int32
}

// Arguments is the worker's argv after the command.
func Arguments(i Identity) []string {
	return []string{strconv.FormatUint(uint64(i.UID), 10), strconv.FormatUint(uint64(i.GID), 10), strconv.FormatInt(int64(i.Keyring), 10)}
}

// parseArgs accepts exactly two canonical nonzero decimal IDs and a canonical
// nonnegative keyring serial.
func parseArgs(args []string) (Identity, error) {
	if len(args) != 3 {
		return Identity{}, fmt.Errorf("%w: arguments", ErrWorker)
	}
	var ids [2]uint32
	for k, s := range args[:2] {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil || v == 0 || strconv.FormatUint(v, 10) != s {
			return Identity{}, fmt.Errorf("%w: arguments", ErrWorker)
		}
		ids[k] = uint32(v)
	}
	keyring, err := strconv.ParseInt(args[2], 10, 32)
	if err != nil || keyring < 0 || strconv.FormatInt(keyring, 10) != args[2] {
		return Identity{}, fmt.Errorf("%w: arguments", ErrWorker)
	}
	return Identity{ids[0], ids[1], int32(keyring)}, nil
}

// verifyKeyring requires the worker's session keyring to be a live keyring
// that is not root's: current is its serial, root the serial root passed.
func verifyKeyring(current, root int32) error {
	if current <= 0 || current == root {
		return fmt.Errorf("%w: session keyring", ErrWorker)
	}
	return nil
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

// verifySession parses /proc/self/stat and requires the process to be pid, to
// lead its own session (session ID equals pid) and to have no controlling tty
// (tty_nr 0). The command name may hold spaces and parentheses, so fields are
// read after its last closing parenthesis.
func verifySession(raw []byte, pid int) error {
	text := string(raw)
	open, end := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if len(raw) > 4096 || open < 1 || end < open {
		return fmt.Errorf("%w: session", ErrWorker)
	}
	self, err := strconv.Atoi(strings.TrimSpace(text[:open]))
	f := strings.Fields(text[end+1:])
	// state ppid pgrp session tty_nr ...
	if err != nil || self != pid || len(f) < 5 {
		return fmt.Errorf("%w: session", ErrWorker)
	}
	session, serr := strconv.Atoi(f[3])
	tty, terr := strconv.Atoi(f[4])
	if serr != nil || session != pid {
		return fmt.Errorf("%w: session", ErrWorker)
	}
	if terr != nil || tty != 0 {
		return fmt.Errorf("%w: controlling tty", ErrWorker)
	}
	return nil
}
