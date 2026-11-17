package cleanworker

import (
	"errors"
	"strings"
	"testing"
)

const goodStatus = "Name:\tbench-quota\nUmask:\t0022\nState:\tR (running)\nUid:\t61001\t61001\t61001\t61001\nGid:\t61003\t61003\t61003\t61003\nFDSize:\t64\nGroups:\t\nCapInh:\t0000000000000000\nCapPrm:\t0000000000000000\nCapEff:\t0000000000000000\nCapBnd:\t0000000000000000\nCapAmb:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t0\n"

var daemon = Identity{61001, 61003, 0}

func TestStatusRefusesAnyCredentialTheDropDidNotProduce(t *testing.T) {
	if err := verifyStatus([]byte(goodStatus), daemon); err != nil {
		t.Fatal("dropped status refused", err)
	}
	for name, c := range map[string]struct{ from, to, reason string }{
		"real-uid":      {"Uid:\t61001", "Uid:\t1000", "Uid"},
		"saved-uid":     {"61001\t61001\nGid", "61001\t0\nGid", "Uid"},
		"fs-gid":        {"61003\t61003\nFDSize", "61003\t0\nFDSize", "Gid"},
		"extra-group":   {"Groups:\t\n", "Groups:\t27\n", "Groups"},
		"root-group":    {"Groups:\t\n", "Groups:\t0\n", "Groups"},
		"inheritable":   {"CapInh:\t0000000000000000", "CapInh:\t0000000000000001", "CapInh"},
		"permitted":     {"CapPrm:\t0000000000000000", "CapPrm:\t0000000000000400", "CapPrm"},
		"effective":     {"CapEff:\t0000000000000000", "CapEff:\t000001ffffffffff", "CapEff"},
		"bounding":      {"CapBnd:\t0000000000000000", "CapBnd:\t000001ffffffffff", "CapBnd"},
		"ambient":       {"CapAmb:\t0000000000000000", "CapAmb:\t0000000000000001", "CapAmb"},
		"privs":         {"NoNewPrivs:\t1", "NoNewPrivs:\t0", "NoNewPrivs"},
		"missing-privs": {"NoNewPrivs:\t1\n", "", "NoNewPrivs"},
		"missing-uid":   {"Uid:\t61001\t61001\t61001\t61001\n", "", "Uid"},
		"duplicate":     {"Groups:\t\n", "Groups:\t\nGroups:\t\n", "duplicate"},
	} {
		raw := strings.Replace(goodStatus, c.from, c.to, 1)
		if raw == goodStatus {
			t.Fatalf("%s: fixture replacement did not apply", name)
		}
		err := verifyStatus([]byte(raw), daemon)
		if !errors.Is(err, ErrWorker) || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s status admitted or misreported: %v", name, err)
		}
	}
	if err := verifyStatus([]byte(goodStatus), Identity{61002, 61003, 0}); !errors.Is(err, ErrWorker) {
		t.Error("another uid admitted", err)
	}
	if err := verifyStatus([]byte(goodStatus+strings.Repeat("x", 64<<10)), daemon); !errors.Is(err, ErrWorker) {
		t.Error("oversize status admitted", err)
	}
}

func TestDescriptorsMustBeExactlyStdio(t *testing.T) {
	if err := verifyDescriptors([]string{".", "..", "0", "1", "2", "3"}, 3); err != nil {
		t.Fatal("stdio plus the listing descriptor refused", err)
	}
	for name, names := range map[string][]string{
		"inherited":    {"0", "1", "2", "3", "4"},
		"no-stderr":    {"0", "1", "3"},
		"stderr-moved": {"0", "1", "5", "3"},
		"high":         {"0", "1", "2", "3", "1023"},
		"padded":       {"0", "1", "02", "3"},
		"not-a-number": {"0", "1", "2", "3", "x"},
		"negative":     {"0", "1", "2", "3", "-1"},
	} {
		if err := verifyDescriptors(names, 3); !errors.Is(err, ErrWorker) {
			t.Errorf("%s descriptors admitted: %v", name, err)
		}
	}
}

func TestArgumentsAreTwoCanonicalNonzeroIDs(t *testing.T) {
	if got, err := parseArgs(Arguments(daemon)); err != nil || got != daemon {
		t.Fatal("canonical arguments refused", got, err)
	}
	withKeyring := Identity{61001, 61003, 812345}
	if got, err := parseArgs(Arguments(withKeyring)); err != nil || got != withKeyring {
		t.Fatal("keyring serial refused", got, err)
	}
	for _, bad := range [][]string{nil, {"61001", "61003"}, {"61001", "61003", "0", "x"}, {"61001", "61003", "x"}, {"0", "61003", "0"}, {"61001", "0", "0"}, {"061001", "61003", "0"}, {"+61001", "61003", "0"}, {"4294967296", "1", "0"}, {"-1", "1", "0"}, {"61001", "61003", "-3"}, {"61001", "61003", "07"}, {"61001", "61003", "2147483648"}} {
		if _, err := parseArgs(bad); !errors.Is(err, ErrWorker) {
			t.Errorf("arguments %q admitted: %v", bad, err)
		}
	}
}

func TestSessionRequiresALeaderWithoutAControllingTTY(t *testing.T) {
	good := "4242 (bench-quota) S 4100 4242 4242 0 -1 4194560 120 0 0 0\n"
	if err := verifySession([]byte(good), 4242); err != nil {
		t.Fatal("own session without a tty refused", err)
	}
	odd := "4242 (a) b (c)) R 4100 4242 4242 0 -1 0\n"
	if err := verifySession([]byte(odd), 4242); err != nil {
		t.Fatal("command name with parentheses misparsed", err)
	}
	for name, c := range map[string]struct {
		stat   string
		pid    int
		reason string
	}{
		"operator-session": {"4242 (bench-quota) S 4100 4100 4000 34816 4100 0\n", 4242, ": session"},
		"root-tty":         {"4242 (bench-quota) S 4100 4242 4242 34816 -1 0\n", 4242, "controlling tty"},
		"other-process":    {good, 4243, ": session"},
		"name-hides-tty":   {"4242 (x) S 1 4242 4242 34816 0 0) S 1 4242 4242 0 -1 0\n", 4242, ""},
		"truncated":        {"4242 (bench-quota) S 4100 4242\n", 4242, ": session"},
		"no-name":          {"4242 bench-quota S 4100 4242 4242 0\n", 4242, ": session"},
		"bad-tty":          {"4242 (bench-quota) S 4100 4242 4242 x -1 0\n", 4242, "controlling tty"},
	} {
		err := verifySession([]byte(c.stat), c.pid)
		if name == "name-hides-tty" {
			// The last parenthesis ends the name: the trailing fields decide.
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if !errors.Is(err, ErrWorker) || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s admitted or misreported: %v", name, err)
		}
	}
}

// The worker must hold a live session keyring that is not root's own.
func TestKeyringMustBeLiveAndNotRoots(t *testing.T) {
	if err := verifyKeyring(812346, 812345); err != nil {
		t.Fatal("fresh keyring refused", err)
	}
	if err := verifyKeyring(812346, 0); err != nil {
		t.Fatal("fresh keyring refused when root had none", err)
	}
	for name, c := range map[string][2]int32{"root's": {812345, 812345}, "none": {0, 812345}, "negative": {-3, 0}} {
		if err := verifyKeyring(c[0], c[1]); !errors.Is(err, ErrWorker) {
			t.Errorf("%s keyring admitted: %v", name, err)
		}
	}
}
