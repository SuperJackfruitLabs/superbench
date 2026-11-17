package hoststorage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func retirementFixture(t *testing.T, absent bool) (Worker, RetirementApproval) {
	t.Helper()
	w, assignment := storageFixture(t)
	s, err := w.InspectStorage(context.Background(), "source", assignment)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(w.Data, "benches"), 0755); err != nil {
		t.Fatal(err)
	}
	if !absent {
		if err = os.Mkdir(assignment.Path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := HostConfig{DataRoot: w.Data, QuotaHelper: "/fixed/quota-helper", MinDataFreeBytes: 1024, MinStateFreeBytes: 1024}.Defaults()
	raw, _ := json.Marshal(c)
	if err = os.WriteFile(filepath.Join(w.State, "host.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	s.Managed = true
	s.FilesystemUUID = "11111111-1111-1111-1111-111111111111"
	s.PolicySHA256, s.LedgerSHA256, s.AnchorSHA256, s.ConfigSHA256 = strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), storageDigest(raw)
	s.Assignment.ProjectID, s.Assignment.Bytes, s.Assignment.Inodes = 10000, 8<<20, 128
	r, err := CaptureRetirementRoot(s)
	if err != nil {
		t.Fatal(err)
	}
	a := RetirementApproval{Version: RetirementVersion, Key: "source", Storage: s, Root: r}
	if _, err = a.Bytes(); err != nil {
		t.Fatal(err)
	}
	return w, a
}

func TestRetirementAbsenceRejectsEvenEmptyReappearanceAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"empty", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			_, a := retirementFixture(t, true)
			parent, err := openRetirementParent(a.Storage, &a.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			if absent, err := checkRetirementRoot(parent, a, true); err != nil || !absent {
				t.Fatal("initial absence not established", err)
			}
			path := a.Storage.Assignment.Path
			switch kind {
			case "empty":
				err = os.Mkdir(path, 0700)
			case "file":
				err = os.WriteFile(path, []byte("preserve returned bytes"), 0600)
			case "symlink":
				err = os.Symlink(a.Storage.Root, path)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = checkRetirementRoot(parent, a, true); err == nil {
				t.Fatal("reappeared source accepted")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("refusal altered returned path", err)
			}
			if kind == "file" {
				raw, _ := os.ReadFile(path)
				if string(raw) != "preserve returned bytes" {
					t.Fatal("returned data changed")
				}
			}
		})
	}
}

func TestRetirementPinsOriginalRootAndParentTypeIdentityAndModes(t *testing.T) {
	for _, kind := range []string{"root-mode", "root-replaced", "parent-mode", "parent-replaced", "parent-symlink", "parent-absent"} {
		t.Run(kind, func(t *testing.T) {
			_, a := retirementFixture(t, false)
			parentPath := filepath.Dir(a.Storage.Assignment.Path)
			parent, err := openRetirementParent(a.Storage, &a.Root)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			switch kind {
			case "root-mode":
				err = os.Chmod(a.Storage.Assignment.Path, 0500)
			case "root-replaced":
				err = os.Rename(a.Storage.Assignment.Path, a.Storage.Assignment.Path+"-preserved")
				if err == nil {
					err = os.Mkdir(a.Storage.Assignment.Path, 0700)
				}
			case "parent-mode":
				err = os.Chmod(parentPath, 0700)
			case "parent-replaced":
				err = os.Rename(parentPath, parentPath+"-preserved")
				if err == nil {
					err = os.Mkdir(parentPath, 0755)
				}
			case "parent-symlink":
				err = os.Rename(parentPath, parentPath+"-preserved")
				if err == nil {
					err = os.Symlink(parentPath+"-preserved", parentPath)
				}
			case "parent-absent":
				err = os.Rename(parentPath, parentPath+"-preserved")
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "parent-") {
				current, e := openRetirementParent(a.Storage, &a.Root)
				if e == nil {
					current.Close()
					t.Fatal("changed/missing parent became leaf absence")
				}
			} else if _, e := checkRetirementRoot(parent, a, false); e == nil {
				t.Fatal("changed root accepted")
			}
		})
	}
}

func TestRetirementApprovalCanonicalAndCannotReconstructFromTombstone(t *testing.T) {
	_, a := retirementFixture(t, true)
	raw, err := a.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRetirementApproval(raw)
	if err != nil || parsed != a {
		t.Fatal("canonical roundtrip", err)
	}
	for _, bad := range [][]byte{append(raw, '\n'), append(raw, raw...), append(raw[:len(raw)-1], []byte(`,"Key":"other"}`)...), []byte(strings.Repeat("x", MaxRetirementApproval+1))} {
		if _, err := ParseRetirementApproval(bad); err == nil {
			t.Fatal("ambiguous/unbounded approval accepted")
		}
	}
	for _, kind := range []string{"zero-cap", "zero-id", "key", "mixed-absence", "parent-type", "root-type"} {
		t.Run(kind, func(t *testing.T) {
			b := a
			switch kind {
			case "zero-cap":
				b.Storage.Assignment.Bytes = 0
			case "zero-id":
				b.Storage.Assignment.ProjectID = 0
			case "key":
				b.Key = "other"
			case "mixed-absence":
				b.Root.Inode = 1
			case "parent-type":
				b.Root.ParentMode = 0600
			case "root-type":
				b.Root.Absent = false
				b.Root.Device = b.Storage.Device
				b.Root.Inode = 1
				b.Root.Mode = uint32(os.ModeSymlink) | 0700
			}
			if _, err := b.Bytes(); err == nil {
				t.Fatal("invalid retirement approval accepted")
			}
		})
	}
}

func TestRetirementWorkerUsesFixedHelperAndOriginalCanonicalApproval(t *testing.T) {
	for _, verify := range []bool{false, true} {
		t.Run(map[bool]string{false: "retire", true: "verify"}[verify], func(t *testing.T) {
			w, a := retirementFixture(t, true)
			approved, _ := a.Bytes()
			for _, kind := range []string{"valid", "wrong-id", "active", "wrong-path", "duplicate", "oversized", "config-change"} {
				t.Run(kind, func(t *testing.T) {
					called := false
					run := func(_ context.Context, input []byte, binary string, args ...string) ([]byte, error) {
						called = true
						if !bytes.Equal(input, approved) {
							t.Fatal("approval changed in transit")
						}
						operation := "retire-approved"
						if verify {
							operation = "verify-retired"
						}
						expected := []string{operation, "bench", "source"}
						wantBinary := "/fixed/quota-helper"
						if os.Geteuid() != 0 {
							expected = append([]string{"-n", wantBinary}, expected...)
							wantBinary = "/usr/bin/sudo"
						}
						if binary != wantBinary || !reflect.DeepEqual(args, expected) {
							t.Fatalf("non-fixed helper call: %s %q", binary, args)
						}
						out := a.Storage.Assignment
						out.Bytes, out.Inodes = 0, 0
						switch kind {
						case "wrong-id":
							out.ProjectID++
						case "active":
							out.Bytes = 1024
						case "wrong-path":
							out.Path += "-other"
						}
						raw, _ := json.Marshal(out)
						if kind == "duplicate" {
							raw = append(raw[:len(raw)-1], []byte(`,"Bytes":0}`)...)
						}
						if kind == "oversized" {
							raw = []byte(strings.Repeat("x", MaxStorageIdentity+1))
						}
						return raw, nil
					}
					input := a
					if kind == "config-change" {
						input.Storage.ConfigSHA256 = strings.Repeat("f", 64)
					}
					err := w.retirement(context.Background(), input, verify, run)
					if kind == "valid" {
						if err != nil {
							t.Fatal(err)
						}
					} else if err == nil {
						t.Fatal("conflicting helper authority accepted")
					}
					if kind == "config-change" && called {
						t.Fatal("changed host policy invoked helper")
					}
					entries, _ := os.ReadDir(w.Data)
					if len(entries) != 1 || entries[0].Name() != "benches" {
						t.Fatal("worker changed source files", entries)
					}
				})
			}
		})
	}
}

func TestRetirementNeverDeletesEvenOriginalApprovedEmptyRoot(t *testing.T) {
	w, a := retirementFixture(t, false)
	called := false
	run := func(context.Context, []byte, string, ...string) ([]byte, error) {
		called = true
		z := a.Storage.Assignment
		z.Bytes, z.Inodes = 0, 0
		return json.Marshal(z)
	}
	info, err := os.Stat(a.Storage.Assignment.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.retirement(context.Background(), a, false, run); err == nil || called {
		t.Fatal("present empty approved root reached helper", err)
	}
	after, err := os.Stat(a.Storage.Assignment.Path)
	if err != nil || !os.SameFile(info, after) {
		t.Fatal("retirement deleted original empty root", err)
	}
	// Only this test owns/removes its empty fixture. The SAME original approval
	// remains usable once archive has performed its independently approved removal.
	if err = os.Remove(a.Storage.Assignment.Path); err != nil {
		t.Fatal(err)
	}
	if err = w.retirement(context.Background(), a, false, run); err != nil || !called {
		t.Fatal("absent fixed allocation refused", err)
	}
	called = false
	if err = os.Mkdir(a.Storage.Assignment.Path, 0700); err != nil {
		t.Fatal(err)
	}
	// No inode comparison can waive current absence, even if the kernel reuses it.
	for _, verify := range []bool{false, true} {
		if err = w.retirement(context.Background(), a, verify, run); err == nil || called {
			t.Fatal("reappeared empty root reached helper", err)
		}
	}
	if _, err = os.Stat(a.Storage.Assignment.Path); err != nil {
		t.Fatal("returned empty directory removed", err)
	}
}

func TestRetirementAuthorityReadOnlyAllowsPresentLeafButNeverGrantsRetirement(t *testing.T) {
	w, a := retirementFixture(t, false)
	sentinel := filepath.Join(a.Storage.Assignment.Path, "returned-private")
	if err := os.WriteFile(sentinel, []byte("preserve even unapproved returned bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []bool{false, true} {
		for _, bad := range []bool{false, true} {
			called := false
			run := func(_ context.Context, _ []byte, _ string, args ...string) ([]byte, error) {
				called = true
				found := false
				for _, arg := range args {
					found = found || arg == "verify-retirement-authority"
				}
				if !found {
					t.Fatal("wrong read-only helper operation")
				}
				out := a.Storage.Assignment
				if retired {
					out.Bytes, out.Inodes = 0, 0
				}
				if bad {
					out.ProjectID++
				}
				return json.Marshal(out)
			}
			err := w.retirementOperation(context.Background(), a, "verify-retirement-authority", run)
			if !called || (err != nil) != bad {
				t.Fatal("authority selected binding result", retired, bad, err)
			}
			contents, _ := os.ReadFile(sentinel)
			if string(contents) != "preserve even unapproved returned bytes" {
				t.Fatal("authority probe altered present root")
			}
		}
	}
	called := false
	run := func(context.Context, []byte, string, ...string) ([]byte, error) { called = true; return nil, nil }
	if err := w.retirement(context.Background(), a, false, run); err == nil || called {
		t.Fatal("authority probe waived strict retirement absence")
	}
}

// A retirement approved before a reboot renumbered the data device still opens
// its managed parent and root; unmanaged evidence has no UUID and refuses.
func TestRetirementApprovedBeforeARenumberedDevice(t *testing.T) {
	_, a := retirementFixture(t, false)
	a.Storage.Device, a.Root.Device, a.Root.ParentDevice = a.Storage.Device+7, a.Root.Device+7, a.Root.ParentDevice+7
	parent, err := openRetirementParent(a.Storage, &a.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if absent, err := checkRetirementRoot(parent, a, false); err != nil || absent {
		t.Fatalf("renumbered approved root: %v %v", absent, err)
	}
	a.Storage.Managed = false
	if _, err = openRetirementParent(a.Storage, &a.Root); err == nil {
		t.Fatal("unmanaged evidence with another device opened")
	}
}
