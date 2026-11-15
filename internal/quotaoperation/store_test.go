package quotaoperation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (Store, Identity) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	i := Identity{OperationID: strings.Repeat("a", 64), Epoch: strings.Repeat("b", 32), StateIdentity: strings.Repeat("c", 64), Authority: strings.Repeat("d", 64), BinarySHA256: strings.Repeat("e", 64)}
	s := Store{Directory: root, Check: func(got Identity) error {
		if got != i {
			return errors.New("pinned policy/root/helper changed")
		}
		return nil
	}, uid: uint32(os.Geteuid())}
	return s, i
}
func TestQueuedTypedHelperCannotMutateAfterDurableCancellation(t *testing.T) {
	s, i := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	queued := s
	first := true
	queued.Check = func(got Identity) error {
		if first {
			first = false
			close(entered)
			<-release
		}
		return s.Check(got)
	}
	effects := 0
	done := make(chan error, 1)
	go func() { done <- queued.Apply(i, func() error { effects++; return nil }) }()
	<-entered
	proof, err := s.Cancel(i)
	if err != nil || !proof.Cancelled || proof.Identity != i {
		t.Fatal(proof, err)
	}
	close(release)
	if err = <-done; !errors.Is(err, ErrCancelled) || effects != 0 {
		t.Fatal("queued helper mutated after cancellation proof", effects, err)
	}
	restarted := s
	if err = restarted.Apply(i, func() error { effects++; return nil }); !errors.Is(err, ErrCancelled) || effects != 0 {
		t.Fatal("restart lost deny barrier", effects, err)
	}
}
func TestCancellationWaitsForEntireAlreadyAdmittedApply(t *testing.T) {
	s, i := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	applyDone := make(chan error, 1)
	go func() { applyDone <- s.Apply(i, func() error { close(entered); <-release; return nil }) }()
	<-entered
	cancelDone := make(chan error, 1)
	go func() { _, err := s.Cancel(i); cancelDone <- err }()
	select {
	case err := <-cancelDone:
		t.Fatal("helper effects still active when barrier acknowledged", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(i, func() error { t.Fatal("later helper executed"); return nil }); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
}
func TestMarkerPrecedesEffectsAndSurvivesFailureWithoutAuthorityReuse(t *testing.T) {
	s, i := fixture(t)
	failed := errors.New("controller lost after partial effects")
	err := s.Apply(i, func() error {
		marker, e := readMarker(filepath.Join(s.Directory, "operations", i.OperationID+".json"), s.uid)
		if e != nil || marker.Identity != i || marker.State != "0" {
			t.Fatal("effects preceded durable known marker", marker, e)
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	other := i
	other.Epoch = strings.Repeat("f", 32)
	if _, err = s.Cancel(other); err == nil {
		t.Fatal("new epoch adopted old marker")
	}
	if _, err = s.Cancel(i); err != nil {
		t.Fatal("exact crash retry could not barrier", err)
	}
}
func TestChangedPolicyOrUnknownMetadataCannotProduceCancellationProof(t *testing.T) {
	for _, fault := range []string{"authority", "metadata"} {
		t.Run(fault, func(t *testing.T) {
			s, i := fixture(t)
			if err := s.Apply(i, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if fault == "authority" {
				i.Authority = strings.Repeat("f", 64)
			} else if err := os.WriteFile(filepath.Join(s.Directory, "operations", "unknown"), []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if proof, err := s.Cancel(i); err == nil || proof.Cancelled {
				t.Fatal("changed authority acknowledged", proof, err)
			}
			if fault == "metadata" {
				raw, err := os.ReadFile(filepath.Join(s.Directory, "operations", "unknown"))
				if err != nil || string(raw) != "preserve" {
					t.Fatal("unknown evidence removed", err)
				}
			}
		})
	}
}

func TestRetainedPendingReservesIdentityWithoutReplayOrOverwrite(t *testing.T) {
	s, i := fixture(t)
	dir := filepath.Join(s.Directory, "operations")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(dir, "pending-"+i.OperationID+"-"+i.Epoch+"-12345")
	raw, err := json.Marshal(marker{Version: Version, Identity: i, State: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(pending, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(i); err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(i, func() error { t.Fatal("old pending replayed after denial"); return nil }); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(pending)
	if err != nil || string(after) != string(raw) {
		t.Fatal("pending intent was adopted/deleted/rewritten", err)
	}
	got, count, reserved, err := inventory(dir, i, s.uid)
	if err != nil || count != 1 || !reserved || got == nil || got.State != "1" {
		t.Fatal(got, count, reserved, err)
	}
}

func TestDenialWriteOrSyncFailureNeverAcknowledgesAndRetryIsMonotonic(t *testing.T) {
	for _, fault := range []string{"short", "sync", "torn"} {
		t.Run(fault, func(t *testing.T) {
			s, i := fixture(t)
			if err := s.Apply(i, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			failed := s
			switch fault {
			case "short":
				failed.denyIO.write = func(*os.File, []byte, int64) (int, error) { return 0, nil }
			case "sync":
				failed.denyIO.sync = func(*os.File) error { return errors.New("sync fault") }
			case "torn":
				failed.denyIO.write = func(f *os.File, _ []byte, offset int64) (int, error) { return f.WriteAt([]byte("2"), offset) }
			}
			if proof, err := failed.Cancel(i); err == nil || proof.Cancelled {
				t.Fatal("failed denial acknowledged", proof, err)
			}
			if fault == "torn" {
				if proof, err := s.Cancel(i); err == nil || proof.Cancelled {
					t.Fatal("noncanonical state adopted", proof, err)
				}
				if err := s.Apply(i, func() error { t.Fatal("corrupt denial admitted effects"); return nil }); err == nil {
					t.Fatal("corrupt marker accepted")
				}
				return
			}
			if proof, err := s.Cancel(i); err != nil || !proof.Cancelled {
				t.Fatal("exact denial retry failed", proof, err)
			}
			if err := s.Apply(i, func() error { t.Fatal("apply overwrote denial"); return nil }); !errors.Is(err, ErrCancelled) {
				t.Fatal(err)
			}
		})
	}
}

func TestKnownCancellationWorksAtFullMarkerBudget(t *testing.T) {
	s, i := fixture(t)
	dir := filepath.Join(s.Directory, "operations")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A complete durable pending record reserves the slot even if its helper
	// crashed before fixed-name publication. Cancellation must not replay it.
	pending, err := json.Marshal(marker{Version: Version, Identity: i, State: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "pending-"+i.OperationID+"-"+i.Epoch+"-12345"), pending, 0600); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < MaxMarkers-1; n++ {
		another := i
		another.OperationID = fmt.Sprintf("%064x", n)
		raw, err := json.Marshal(marker{Version: Version, Identity: another, State: "0"})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, another.OperationID+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if proof, err := s.Cancel(i); err != nil || !proof.Cancelled {
		t.Fatal("full budget blocked existing cancellation", proof, err)
	}
	unknown := i
	unknown.OperationID = strings.Repeat("f", 64)
	permissive := s
	permissive.Check = func(got Identity) error {
		expected := i
		expected.OperationID = got.OperationID
		if got != expected {
			return ErrAuthority
		}
		return nil
	}
	if proof, err := permissive.Cancel(unknown); !errors.Is(err, ErrCapacity) || proof.Cancelled {
		t.Fatal("unknown target overran bounded inventory", proof, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, unknown.OperationID+".json")); !os.IsNotExist(err) {
		t.Fatal("failed cancellation created marker", err)
	}
	if proof, err := s.Cancel(i); err != nil || !proof.Cancelled {
		t.Fatal("known denial retry blocked", proof, err)
	}
}

func TestReplacementMarkerCannotBeModifiedByPriorObservation(t *testing.T) {
	s, i := fixture(t)
	if err := s.Apply(i, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Directory, "operations", i.OperationID+".json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	changed := false
	s.Check = func(Identity) error {
		calls++
		if calls == 3 {
			tmp := path + "-replacement"
			if err := os.WriteFile(tmp, original, 0600); err != nil {
				return err
			}
			if err := os.Rename(tmp, path); err != nil {
				return err
			}
			changed = true
		}
		return nil
	}
	if proof, err := s.Cancel(i); err == nil || proof.Cancelled || !changed {
		t.Fatal("replacement adopted", proof, changed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatal("replacement marker changed", err)
	}
}
