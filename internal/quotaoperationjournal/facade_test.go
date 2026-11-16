package quotaoperationjournal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

func initializedFixture(t *testing.T) *Store {
	t.Helper()
	opened, err := freshFixture(t).InitializeFresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return opened
}

func TestRawFacadeNeverInitializesMissingHistory(t *testing.T) {
	raw, _ := fixture(t)
	s := &Store{}
	effects := 0
	if err := s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) {
		t.Fatalf("raw apply:%v", err)
	}
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, ErrAuthority) || proof != (quotaoperation.Proof{}) || effects != 0 {
		t.Fatalf("raw cancel:%+v %v count%d", proof, err, effects)
	}
	entries, err := os.ReadDir(raw.Directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("raw initialized history:%v %v", entries, err)
	}
	if err = s.Apply(context.Background(), nil); !errors.Is(err, ErrAuthority) {
		t.Fatalf("nil effect:%v", err)
	}
}

func TestFacadeRetainsReservationAfterEffectFailure(t *testing.T) {
	s := initializedFixture(t)
	i := s.identity
	effectErr := errors.New("fixed effect failed")
	if err := s.Apply(context.Background(), func() error { return effectErr }); !errors.Is(err, effectErr) {
		t.Fatalf("effect error:%v", err)
	}
	backend, err := s.backend(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.withinExisting(context.Background(), func(j *Journal) error {
		found, denied, err := j.Lookup(i)
		if err != nil || !found || denied {
			t.Fatalf("reservation changed:%v %v %v", found, denied, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	proof, err := s.Cancel(context.Background())
	if err != nil || proof != (quotaoperation.Proof{Version: quotaoperation.Version, Identity: i, Cancelled: true}) {
		t.Fatalf("original proof:%+v %v", proof, err)
	}
	effects := 0
	if err = s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, quotaoperation.ErrCancelled) || effects != 0 {
		t.Fatalf("denied retry:%v count%d", err, effects)
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
		t.Fatal("apply did not enter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	proof, err := s.Cancel(ctx)
	close(release)
	applyErr := <-finished
	if !errors.Is(err, context.DeadlineExceeded) || proof != (quotaoperation.Proof{}) || applyErr != nil {
		t.Fatalf("cancel crossed effect:%+v %v apply%v", proof, err, applyErr)
	}
	if _, err = s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFacadeFinalAuthorityFailureNeverReturnsSuccess(t *testing.T) {
	s := initializedFixture(t)
	effects := 0
	err := s.Apply(context.Background(), func() error {
		effects++
		return os.Remove(filepath.Join(s.control.config.Directory, "operations.lock"))
	})
	if !errors.Is(err, ErrAuthority) || effects != 1 {
		t.Fatalf("lost lock acknowledged:%v count%d", err, effects)
	}
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, os.ErrNotExist) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("lost lock proof:%+v %v", proof, err)
	}
}

func TestFacadeRevokedOriginalAuthorityNeverReturnsProof(t *testing.T) {
	first := initializedFixture(t)
	valid := true
	authorityErr := errors.New("original helper authority revoked")
	s, err := first.control.Operation(context.Background(), first.identity, func(quotaoperation.Identity) error {
		if !valid {
			return authorityErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	valid = false
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, authorityErr) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("revoked proof:%+v %v", proof, err)
	}
}

func TestRawFacadeCannotBypassPermanentAnchor(t *testing.T) {
	raw, _ := fixture(t)
	create(t, raw)
	s := &Store{}
	effects := 0
	if err := s.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
		t.Fatalf("raw effects:%v count%d", err, effects)
	}
	proof, err := s.Cancel(context.Background())
	if !errors.Is(err, ErrAuthority) || proof != (quotaoperation.Proof{}) {
		t.Fatalf("raw proof:%+v %v", proof, err)
	}
}

func TestOperationFreezesRealIdentity(t *testing.T) {
	first := initializedFixture(t)
	original := first.identity
	input := original
	s, err := first.control.Operation(context.Background(), input, func(got quotaoperation.Identity) error {
		if got != original {
			return ErrAuthority
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	input.Epoch = strings.Repeat("f", 32)
	proof, err := s.Cancel(context.Background())
	if err != nil || proof.Identity != original || proof.Identity == input || !proof.Cancelled {
		t.Fatalf("identity changed:%+v %v", proof, err)
	}
}

func TestFacadeDuplicateOperationIDNeverRunsForeignIdentity(t *testing.T) {
	for _, field := range []string{"epoch", "state", "binary"} {
		t.Run(field, func(t *testing.T) {
			original := initializedFixture(t)
			if err := original.Apply(context.Background(), func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			foreign := original.identity
			switch field {
			case "epoch":
				foreign.Epoch = strings.Repeat("f", 32)
			case "state":
				foreign.StateIdentity = strings.Repeat("f", 64)
			case "binary":
				foreign.BinarySHA256 = strings.Repeat("f", 64)
			}
			if !foreign.Valid() || foreign == original.identity || foreign.OperationID != original.identity.OperationID {
				t.Fatal("invalid clash fixture")
			}
			candidate, err := original.control.Operation(context.Background(), foreign, func(got quotaoperation.Identity) error {
				if got != foreign {
					return ErrAuthority
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			effects := 0
			if err = candidate.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, ErrAuthority) || effects != 0 {
				t.Fatalf("foreign canonical identity reached effect: %v effects%d", err, effects)
			}
			backend, err := original.backend(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = backend.withinExisting(context.Background(), func(j *Journal) error {
				found, denied, e := j.Lookup(original.identity)
				if e != nil || !found || denied {
					t.Fatalf("original reservation changed: %v found%v denied%v", e, found, denied)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFacadeRetrySyncsBothPinnedFilesBeforeEffect(t *testing.T) {
	for _, fault := range []string{"none", "journal.db", "journal.db-journal"} {
		t.Run(fault, func(t *testing.T) {
			s := initializedFixture(t)
			if err := s.Apply(context.Background(), func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			var synced []string
			syncErr := errors.New("original reservation sync failed")
			retry := *s
			retry.syncFile = func(f *os.File) error {
				name := filepath.Base(f.Name())
				synced = append(synced, name)
				if name == fault {
					return syncErr
				}
				return f.Sync()
			}
			effects := 0
			err := retry.Apply(context.Background(), func() error {
				effects++
				if len(synced) != 2 || synced[0] != "journal.db" || synced[1] != "journal.db-journal" {
					t.Errorf("retry effect preceded durable reservation: %v", synced)
				}
				return nil
			})
			if fault == "none" {
				if err != nil || effects != 1 || len(synced) != 2 {
					t.Fatalf("durable exact retry refused: %v effects%d syncs%v", err, effects, synced)
				}
			} else {
				want := 1
				if fault == "journal.db-journal" {
					want = 2
				}
				if !errors.Is(err, syncErr) || effects != 0 || len(synced) != want {
					t.Fatalf("sync failure admitted retry: %v effects%d syncs%v", err, effects, synced)
				}
			}
			backend, err := s.backend(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err = backend.withinExisting(context.Background(), func(j *Journal) error {
				found, denied, e := j.Lookup(s.identity)
				if e != nil || !found || denied {
					t.Fatalf("retry changed original reservation: %v found%v denied%v", e, found, denied)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFacadeRetryRechecksAuthorityAfterResync(t *testing.T) {
	s := initializedFixture(t)
	if err := s.Apply(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	retry := *s
	valid := true
	verify := retry.verify
	authorityErr := errors.New("original helper revoked during resync")
	retry.verify = func(i quotaoperation.Identity) error {
		if !valid {
			return authorityErr
		}
		return verify(i)
	}
	retry.syncFile = func(f *os.File) error {
		err := f.Sync()
		if filepath.Base(f.Name()) == "journal.db-journal" {
			valid = false
		}
		return err
	}
	effects := 0
	if err := retry.Apply(context.Background(), func() error { effects++; return nil }); !errors.Is(err, authorityErr) || effects != 0 {
		t.Fatalf("resync bypassed original authority: %v effects%d", err, effects)
	}
}
