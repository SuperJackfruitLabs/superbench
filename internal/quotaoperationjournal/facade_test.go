package quotaoperationjournal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func TestFacadeNeverInitializesMissingHistory(t *testing.T) {
	s, _ := fixture(t)
	effects := 0
	if err := s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) {
		t.Fatalf("missing journal apply: %v", err)
	}
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, ErrAuthority) || proof != (quotaoperation.Proof{}) || effects != 0 {
		t.Fatalf("missing journal cancel: proof=%+v err=%v effects=%d", proof, err, effects)
	}
	entries, err := os.ReadDir(s.Directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing journal was initialized: entries=%v err=%v", entries, err)
	}
	if err = s.Apply(context.Background(), nil); !errors.Is(err, ErrAuthority) {
		t.Fatalf("nil effect: %v", err)
	}
}

func TestFacadeRetainsReservationAfterEffectFailure(t *testing.T) {
	s := initializedFixture(t)
	i := s.Identity
	effectErr := errors.New("fixed effect failed")
	if err := s.Apply(context.Background(), func() error { return effectErr }); !errors.Is(err, effectErr) {
		t.Fatalf("effect error changed: %v", err)
	}
	if err := s.withinExisting(context.Background(), func(j *Journal) error {
		found, denied, err := j.Lookup(i)
		if err != nil || !found || denied {
			t.Fatalf("failed effect lost original reservation: %v %v %v", found, denied, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proof, err := s.Cancel(context.Background())
	if err != nil || proof != (quotaoperation.Proof{Version: quotaoperation.Version, Identity: i, Cancelled: true}) {
		t.Fatalf("original proof mismatch: %+v %v", proof, err)
	}
	effects := 0
	if err = s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, quotaoperation.ErrCancelled) || effects != 0 {
		t.Fatalf("denied retry ran effect: %v effects=%d", err, effects)
	}
}

func TestFacadeCancelHoldsSameLockThroughEffect(t *testing.T) {
	s := initializedFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- s.Apply(context.Background(), func() error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("apply did not enter effect")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	proof, err := s.Cancel(ctx)
	close(release)
	applyErr := <-finished
	if !errors.Is(err, context.DeadlineExceeded) || proof != (quotaoperation.Proof{}) || applyErr != nil {
		t.Fatalf("cancel crossed running effect: proof=%+v err=%v apply=%v", proof, err, applyErr)
	}
	if _, err = s.Cancel(context.Background()); err != nil {
		t.Fatalf("cancel after effect: %v", err)
	}
}

func TestFacadeFinalAuthorityFailureNeverReturnsSuccess(t *testing.T) {
	s := initializedFixture(t)
	effects := 0
	err := s.Apply(context.Background(), func() error {
		effects++
		return os.Remove(filepath.Join(s.Directory, "operations.lock"))
	})
	if !errors.Is(err, ErrAuthority) || effects != 1 {
		t.Fatalf("lost lock acknowledged effect: %v effects=%d", err, effects)
	}
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, os.ErrNotExist) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("lost original lock returned proof: %+v %v", proof, err)
	}
}

func TestFacadeAuthorityRefusalNeverReturnsProof(t *testing.T) {
	s := initializedFixture(t)
	if _, err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	authorityErr := errors.New("original helper authority changed")
	s.Check = func(quotaoperation.Identity) error { return authorityErr }
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, authorityErr) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("changed authority returned proof: %+v %v", proof, err)
	}
}

func initializedFixture(t *testing.T) Store {
	t.Helper()
	s := freshFixture(t)
	opened, err := s.InitializeFresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return opened
}

func TestRawFacadeCannotBypassPermanentAnchor(t *testing.T) {
	raw, _ := fixture(t)
	create(t, raw) // Only internal characterization can create an unanchored DB.
	effects := 0
	if err := raw.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
		t.Fatalf("raw storage authorized effects: %v effects=%d", err, effects)
	}
	proof, err := raw.Cancel(context.Background())
	if !errors.Is(err, ErrAuthority) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("raw storage authorized proof: %+v %v", proof, err)
	}
}

func TestFacadeCannotReplaceConstructorChecks(t *testing.T) {
	s := initializedFixture(t)
	raw, err := os.ReadFile(s.formatPath())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(s.formatPath(), s.formatPath()+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(s.formatPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	s.Check = func(quotaoperation.Identity) error { return nil }
	effects := 0
	if err = s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
		t.Fatalf("replaced Check bypassed pinned anchor: %v count=%d", err, effects)
	}
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, ErrAuthority) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("replaced Check returned proof: %+v %v", proof, err)
	}
}

func TestFacadeCannotRetargetAnchoredStore(t *testing.T) {
	s := initializedFixture(t)
	other, _ := fixture(t)
	create(t, other)
	s.Directory = other.Directory
	effects := 0
	if err := s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
		t.Fatalf("anchored store retargeted raw journal: %v count=%d", err, effects)
	}
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, ErrAuthority) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("retargeted store returned proof: %+v %v", proof, err)
	}
}
