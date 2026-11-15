package hoststorage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func storageFixture(t *testing.T) (Worker, Assignment) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{State: filepath.Join(root, "state"), Data: filepath.Join(root, "data"), Apply: func(HostConfig, string, string, uint64) (Assignment, error) {
		t.Fatal("inspection allocated quota")
		return Assignment{}, nil
	}, Probe: func(string) (Space, error) {
		t.Fatal("inspection used ancestor-falling space probe")
		return Space{}, nil
	}}
	for _, dir := range []string{w.State, w.Data} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return w, Assignment{Path: filepath.Join(w.Data, "benches", "source")}
}

func TestStorageInspectionMissingCheckoutPreservesDirectoriesAndBindsPhysicalRoot(t *testing.T) {
	w, a := storageFixture(t)
	// Missing checkout and even a missing benches parent are valid observations;
	// neither may be materialized merely to verify the data volume.
	s, err := w.InspectStorage(context.Background(), "source", a)
	if err != nil {
		t.Fatal(err)
	}
	if s.Managed || s.FilesystemUUID != "" || s.Mount == "" {
		t.Fatalf("unmanaged evidence misrepresented: %+v", s)
	}
	if err = w.VerifyStorage(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseStorageIdentity(raw)
	if err != nil || parsed != s {
		t.Fatal("proof roundtrip", err)
	}
	for _, dir := range []string{w.State, w.Data} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("inspection changed %s: %v %v", dir, entries, err)
		}
	}
	old := w.Data + "-old"
	if err = os.Rename(w.Data, old); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(w.Data, 0700); err != nil {
		t.Fatal(err)
	}
	if err = w.VerifyStorage(context.Background(), s); err == nil {
		t.Fatal("replacement directory accepted as original data root")
	}
	if err = os.Remove(w.Data); err != nil {
		t.Fatal(err)
	}
	if err = w.VerifyStorage(context.Background(), s); err == nil {
		t.Fatal("missing root accepted through ancestor fallback")
	}
	if err = os.Symlink(old, w.Data); err != nil {
		t.Fatal(err)
	}
	if _, err = w.InspectStorage(context.Background(), "source", a); err == nil {
		t.Fatal("symlink root accepted")
	}
	entries, err := os.ReadDir(old)
	if err != nil || len(entries) != 0 {
		t.Fatal("original data changed", entries, err)
	}
}

func TestStorageInspectionRejectsWrongAllocationAndCanceledContextWithoutEffects(t *testing.T) {
	w, a := storageFixture(t)
	for _, kind := range []string{"key", "path", "project", "bytes", "inodes", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			key, expected, ctx := "source", a, context.Background()
			switch kind {
			case "key":
				key = "../source"
			case "path":
				expected.Path += "-other"
			case "project":
				expected.ProjectID = 10000
			case "bytes":
				expected.Bytes = 1024
			case "inodes":
				expected.Inodes = 128
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := w.InspectStorage(ctx, key, expected); err == nil {
				t.Fatal("unsafe observation accepted")
			}
			for _, dir := range []string{w.State, w.Data} {
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatal("failure created files", entries, err)
				}
			}
		})
	}
}

func TestManagedStorageWorkerUsesReadOnlyFixedHelperAndExactAssignment(t *testing.T) {
	w, a := storageFixture(t)
	a.ProjectID, a.Bytes, a.Inodes = 10000, 8<<20, 128
	c := HostConfig{DataRoot: w.Data, QuotaHelper: "/fixed/quota-helper", MinDataFreeBytes: 1024, MinStateFreeBytes: 1024}.Defaults()
	config, _ := json.Marshal(c)
	if err := os.WriteFile(filepath.Join(w.State, "host.json"), config, 0600); err != nil {
		t.Fatal(err)
	}
	device, inode, err := physicalStorageRoot(w.Data)
	if err != nil {
		t.Fatal(err)
	}
	proof := StorageIdentity{Version: storageVersion, Root: w.Data, BenchKey: "source", Device: device, Inode: inode, Mount: "exact-mount", Managed: true, FilesystemUUID: "11111111-1111-1111-1111-111111111111", PolicySHA256: strings.Repeat("1", 64), LedgerSHA256: strings.Repeat("2", 64), AnchorSHA256: strings.Repeat("3", 64), Assignment: a}
	for _, kind := range []string{"valid", "root", "key", "project", "bytes", "inodes", "device", "managed", "unknown", "missing-root", "duplicate", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			s := proof
			switch kind {
			case "root":
				s.Root += "-other"
			case "key":
				s.BenchKey = "target"
			case "project":
				s.Assignment.ProjectID++
			case "bytes":
				s.Assignment.Bytes += 1024
			case "inodes":
				s.Assignment.Inodes++
			case "device":
				s.Device++
			case "managed":
				s.Managed = false
			}
			calls := 0
			run := func(_ context.Context, binary string, args ...string) ([]byte, error) {
				calls++
				want := []string{"verify", "bench", "source"}
				wantBinary := c.QuotaHelper
				if os.Geteuid() != 0 {
					want = append([]string{"-n", c.QuotaHelper}, want...)
					wantBinary = "/usr/bin/sudo"
				}
				if binary != wantBinary || !reflect.DeepEqual(args, want) {
					t.Fatalf("non-fixed helper call: %s %q", binary, args)
				}
				raw, _ := json.Marshal(s)
				if kind == "missing-root" {
					var fields map[string]any
					_ = json.Unmarshal(raw, &fields)
					delete(fields, "Root")
					raw, _ = json.Marshal(fields)
				}
				if kind == "duplicate" {
					raw = append(raw[:len(raw)-1], []byte(`,"Managed":true}`)...)
				}
				if kind == "unknown" {
					raw = append(raw[:len(raw)-1], []byte(`,"Unexpected":true}`)...)
				}
				if kind == "oversized" {
					return []byte(strings.Repeat("x", MaxStorageIdentity+1)), nil
				}
				return raw, nil
			}
			got, err := w.inspectStorage(context.Background(), "source", a, run)
			if calls != 1 {
				t.Fatal("unexpected helper calls", calls)
			}
			if kind == "valid" {
				if err != nil || got.ConfigSHA256 != storageDigest(config) {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("conflicting helper authority accepted")
			}
			unchanged, _ := os.ReadFile(filepath.Join(w.State, "host.json"))
			if string(unchanged) != string(config) {
				t.Fatal("inspection modified host policy")
			}
			entries, _ := os.ReadDir(w.Data)
			if len(entries) != 0 {
				t.Fatal("inspection materialized data", entries)
			}
		})
	}
}

func TestStorageInspectionRejectsSpecialOrUnboundedHostPolicy(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "oversized", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			w, a := storageFixture(t)
			path := filepath.Join(w.State, "host.json")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink("missing-policy", path)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat(" ", MaxStorageIdentity+1)), 0600)
			case "unknown":
				err = os.WriteFile(path, []byte(`{"Unexpected":true}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.InspectStorage(context.Background(), "source", a); err == nil {
				t.Fatal("unsafe host policy accepted")
			}
			entries, err := os.ReadDir(w.Data)
			if err != nil || len(entries) != 0 {
				t.Fatal("refusal materialized bench", entries, err)
			}
		})
	}
}

func TestStorageIdentityRejectsNoncanonicalOrUnboundedProof(t *testing.T) {
	w, a := storageFixture(t)
	s, err := w.InspectStorage(context.Background(), "source", a)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := s.Bytes()
	for _, bad := range [][]byte{append(raw, '\n'), append([]byte(" "), raw...), append(raw, raw...), append(raw[:len(raw)-1], []byte(`,"Root":"/other"}`)...), []byte(strings.Repeat("x", MaxStorageIdentity+1))} {
		if _, err := ParseStorageIdentity(bad); err == nil {
			t.Fatal("noncanonical proof accepted")
		}
	}
}

func TestStorageCommandBoundsOutputAndPreservesCancellation(t *testing.T) {
	if _, err := storageCommand(context.Background(), "/bin/sh", "-c", `i=0; while [ "$i" -lt 1100 ]; do printf '0123456789012345678901234567890123456789012345678901234567890123456789'; i=$((i+1)); done`); err == nil {
		t.Fatal("unbounded command output accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storageCommand(ctx, "/bin/sh", "-c", "exit 0"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}
