package freshgeneration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaterGenerationsAreBoundedAndNameAnotherWorkspace(t *testing.T) {
	id, previous := strings.Repeat("e", 32), strings.Repeat("a", 32)
	for _, g := range []uint32{2, 3, Max} {
		if !Later(g, previous, id) {
			t.Errorf("generation %d refused", g)
		}
	}
	for name, ok := range map[string]bool{
		"zero": Later(0, previous, id), "one": Later(1, previous, id), "beyond": Later(Max+1, previous, id),
		"no previous": Later(2, "", id), "previous not an ID": Later(2, "card", id), "previous is itself": Later(2, id, id),
	} {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
}

// Each generation's retained files have their own names; the first keeps the
// key's, so records from before generations stay readable where they are.
func TestStemsNameEachGenerationApart(t *testing.T) {
	if Stem("card", 0) != "card" || Stem("card", 2) != "card@2" || Stem("card", 3) != "card@3" {
		t.Fatal(Stem("card", 0), Stem("card", 2), Stem("card", 3))
	}
	if Before(2) != 0 || Before(3) != 2 || Stem("card", Before(3)) != "card@2" {
		t.Fatal(Before(2), Before(3))
	}
}

// A later generation follows only the exact reclaimed generation before it,
// under the same storage authority, at the bench's own leaf.
func TestALaterGenerationFollowsOnlyItsReclaimedPredecessor(t *testing.T) {
	root, authority, previous := "/srv/data/benches/card", strings.Repeat("a", 64), strings.Repeat("1", 32)
	prior := Prior{Key: "card", ID: previous, Phase: "reclaimed", Authority: authority, Path: root, ProjectID: 10000}
	if err := Follows(prior, "card", previous, 2, authority, root); err != nil {
		t.Fatal(err)
	}
	second := prior
	second.Generation, second.ID = 2, strings.Repeat("2", 32)
	if err := Follows(second, "card", second.ID, 3, authority, root); err != nil {
		t.Fatal("third generation", err)
	}
	for name, c := range map[string]struct {
		change     func(*Prior)
		generation uint32
	}{
		"completed":         {func(p *Prior) { p.Phase = "completed" }, 2},
		"reclaiming":        {func(p *Prior) { p.Phase = "reclaiming" }, 2},
		"another workspace": {func(p *Prior) { p.ID = strings.Repeat("9", 32) }, 2},
		"another key":       {func(p *Prior) { p.Key = "other" }, 2},
		"skips one":         {func(*Prior) {}, 3},
		"first generation":  {func(*Prior) {}, 0},
		"another authority": {func(p *Prior) { p.Authority = strings.Repeat("b", 64) }, 2},
		"another leaf":      {func(p *Prior) { p.Path += "x" }, 2},
		"shared project":    {func(p *Prior) { p.ProjectID = 101 }, 2},
	} {
		p := prior
		c.change(&p)
		if err := Follows(p, "card", previous, c.generation, authority, root); !errors.Is(err, ErrGeneration) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestALaterGenerationWaitsForTheLayoutToMoveAside(t *testing.T) {
	state := t.TempDir()
	if err := LayoutAbsent(state, "card"); err != nil {
		t.Fatal("no registry refused", err)
	}
	dir := filepath.Join(state, "payload-layouts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "card.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LayoutAbsent(state, "card"); !errors.Is(err, ErrGeneration) {
		t.Fatalf("published layout accepted: %v", err)
	}
	if err := os.Rename(filepath.Join(dir, "card.json"), filepath.Join(dir, "card."+strings.Repeat("a", 32)+".reclaimed")); err != nil {
		t.Fatal(err)
	}
	if err := LayoutAbsent(state, "card"); err != nil {
		t.Fatal("moved-aside layout refused", err)
	}
}
