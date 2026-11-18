package workspacebootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
)

// A later generation's spec names it and the reclaimed bench it follows; a
// first generation's is the unchanged v1 spec.
func TestSpecificationCarriesTheGeneration(t *testing.T) {
	data, b := durableFixture()
	first, err := Specification(data, b)
	if err != nil || first.Version != payloadbootstrap.Version || first.Generation != 0 {
		t.Fatalf("first generation: %+v %v", first, err)
	}
	b.Workspace.Generation, b.Workspace.Previous = 3, strings.Repeat("e", 32)
	later, err := Specification(data, b)
	if err != nil || later.Version != payloadbootstrap.GenerationVersion || later.Generation != 3 || later.Previous != b.Workspace.Previous || payloadbootstrap.Stem(later) != "card@3" {
		t.Fatalf("later generation: %+v %v", later, err)
	}
}

func TestMoveLayoutAsideMovesOnlyThePredecessorsRegistration(t *testing.T) {
	_, b := durableFixture()
	b.Workspace.Generation, b.Workspace.Previous = 2, strings.Repeat("e", 32)
	state := t.TempDir()
	dir := filepath.Join(state, "payload-layouts")
	path, aside := filepath.Join(dir, "card.json"), filepath.Join(dir, "card."+b.Workspace.Previous+".reclaimed")
	write := func(id string) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"Key":"card","ID":"`+id+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := MoveLayoutAside(state, b); err != nil {
		t.Fatal("no registry:", err)
	}
	write(strings.Repeat("9", 32))
	if err := MoveLayoutAside(state, b); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("a foreign registration moved: %v", err)
	}
	write(b.Workspace.ID)
	if err := MoveLayoutAside(state, b); err != nil {
		t.Fatalf("this generation's own registration: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("this generation's own registration moved", err)
	}
	write(b.Workspace.Previous)
	if err := MoveLayoutAside(state, b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the predecessor's registration still holds the key", err)
	}
	if raw, err := os.ReadFile(aside); err != nil || !strings.Contains(string(raw), b.Workspace.Previous) {
		t.Fatal("the predecessor's registration was not kept", err)
	}
	if err := MoveLayoutAside(state, b); err != nil {
		t.Fatal("a repeat:", err)
	}
	// Never replace evidence already moved aside.
	write(b.Workspace.Previous)
	if err := MoveLayoutAside(state, b); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("an existing moved-aside registration was replaced: %v", err)
	}
	b.Workspace.Generation, b.Workspace.Previous = 0, ""
	if err := MoveLayoutAside(state, b); err != nil {
		t.Fatal("a first generation:", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("a first generation moved a registration", err)
	}
}
