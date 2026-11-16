package creation

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func TestRecordPinsIdentityBeforeEffectsAndPreservesRetry(t *testing.T) {
	f := Files{State: t.TempDir()}
	f.Write = func(_, path string, raw []byte) error { return os.WriteFile(path, raw, 0600) }
	c := Intent{Bench: model.Bench{Key: "card"}, Source: "pinned-source", Base: "main", Recovering: true}
	// This is an explicit legacy intent upgrade, not an inspection side effect.
	old, _ := json.Marshal(c)
	if err := os.WriteFile(f.JournalPath("card"), old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.Record(&c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || !registrationID.MatchString(c.Bench.RegistrationID) {
		t.Fatal("identity not pinned", c)
	}
	raw, err := os.ReadFile(f.JournalPath("card"))
	if err != nil {
		t.Fatal(err)
	}
	var retry Intent
	if err = json.Unmarshal(raw, &retry); err != nil {
		t.Fatal(err)
	}
	if retry.Source != c.Source || retry.Base != c.Base || !retry.Recovering {
		t.Fatal("legacy pinned identity changed")
	}
	retry.Bench.ProjectID = 17
	if err = f.Record(&retry); err != nil {
		t.Fatal(err)
	}
	if retry.Bench.RegistrationID != c.Bench.RegistrationID {
		t.Fatal("retry minted another identity")
	}
	retry.Bench.RegistrationID = "fedcba9876543210fedcba9876543210"
	before, _ := os.ReadFile(f.JournalPath("card"))
	if err = f.Record(&retry); err == nil {
		t.Fatal("registration replacement accepted")
	}
	after, _ := os.ReadFile(f.JournalPath("card"))
	if string(before) != string(after) {
		t.Fatal("rejected retry rewrote intent")
	}
}

func TestIntentWriteFailureDoesNotAuthorizeMaterialization(t *testing.T) {
	writes := 0
	f := Files{State: t.TempDir(), Write: func(string, string, []byte) error { writes++; return errors.New("durable write failed") }}
	c := Intent{Bench: model.Bench{Key: "card"}}
	if err := f.Record(&c); err == nil || writes != 1 {
		t.Fatal("failed journal acknowledged", err)
	}
	if _, err := os.Stat(f.JournalPath("card")); !os.IsNotExist(err) {
		t.Fatal("failed write published intent", err)
	}
	for _, c := range []Intent{{Version: 2}, {Version: 0, Bench: model.Bench{RegistrationID: "0123456789abcdef0123456789abcdef"}}, {Version: 1}} {
		if intentIdentity(&c) == nil {
			t.Fatal("unsupported intent identity accepted", c)
		}
	}
}

func TestLegacyCreationPinsPreviouslyUnknownInodesOnlyOnce(t *testing.T) {
	f := Files{State: t.TempDir(), Write: func(_, path string, raw []byte) error { return os.WriteFile(path, raw, 0600) }}
	c := Intent{Source: "pinned", Bench: model.Bench{Key: "card", ProjectID: 7, QuotaBytes: 4096}}
	if err := f.Record(&c); err != nil {
		t.Fatal(err)
	}
	id := c.Bench.RegistrationID
	c.Bench.QuotaInodes = 128 // Existing quota reconciliation permits legacy zero once.
	if err := f.Record(&c); err != nil {
		t.Fatal("legacy inode confirmation refused", err)
	}
	if c.Bench.RegistrationID != id {
		t.Fatal("inode confirmation changed registration")
	}
	c.Bench.QuotaInodes = 256
	if err := f.Record(&c); err == nil {
		t.Fatal("pinned inode cap replaced")
	}
}
