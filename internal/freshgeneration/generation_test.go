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

// retired is a generation that never completed, whose allocation root retired:
// its intent and root's retirement of exactly that intent.
func retired() (Prior, string, string) {
	root, authority := "/srv/data/benches/card", strings.Repeat("a", 64)
	p := Prior{Key: "card", ID: strings.Repeat("1", 32), Phase: "staging", Authority: authority, Path: root, ProjectID: 10004, Bytes: 3 << 30, Inodes: 4096,
		Operation: `{"OperationID":"o"}`, PolicyDigest: strings.Repeat("b", 64), IntentSHA256: strings.Repeat("c", 64), SpecSHA256: strings.Repeat("d", 64)}
	recorded := Allocation{p.Path, p.ProjectID, p.Bytes, p.Inodes}
	p.Retirement = &Retirement{Key: p.Key, ID: p.ID, Phase: p.Phase, IntentSHA256: p.IntentSHA256, SpecSHA256: p.SpecSHA256, Operation: p.Operation, PolicyDigest: p.PolicyDigest,
		OperationDenied: true, Recorded: recorded, ApprovalKey: p.Key, Authority: authority, Approved: recorded}
	return p, authority, root
}

// reserve makes p a reserved intent's reading: it records no allocation.
func reserve(p *Prior) {
	p.Phase, p.Retirement.Phase = "reserved", "reserved"
	p.Path, p.ProjectID, p.Bytes, p.Inodes, p.Retirement.Recorded = "", 0, 0, 0, Allocation{}
}

// A create that never completed is followed once root retired its allocation:
// root's retirement must name that very intent, made under its denied
// operation, of the allocation it recorded, under the same authority and leaf.
func TestALaterGenerationFollowsARetiredUnfinishedCreate(t *testing.T) {
	prior, authority, root := retired()
	for _, phase := range []string{"reserved", "allocating", "begun", "staging", "materialized_unfenced", "staged", "transferring"} {
		p, _, _ := retired()
		p.Phase, p.Retirement.Phase = phase, phase
		if phase == "reserved" {
			reserve(&p)
		}
		if err := Follows(p, "card", p.ID, 2, authority, root); err != nil {
			t.Errorf("retired %s generation refused: %v", phase, err)
		}
		if a := p.Allocation(); a != (Allocation{Path: root, ProjectID: 10004}) {
			t.Errorf("%s allocation %+v", phase, a)
		}
	}
	second := prior
	second.Generation = 2
	if err := Follows(second, "card", second.ID, 3, authority, root); err != nil {
		t.Fatal("a retired second generation", err)
	}
	t.Log("FAILED_CREATE_RETIRED_PREDECESSOR_FOLLOWED_PASS")
	other := func(a *Allocation) { a.ProjectID = 10005 }
	for name, c := range map[string]struct {
		change     func(*Prior)
		generation uint32
	}{
		"not retired":            {func(p *Prior) { p.Retirement = nil }, 2},
		"completed":              {func(p *Prior) { p.Phase, p.Retirement.Phase = "completed", "completed" }, 2},
		"reclaiming":             {func(p *Prior) { p.Phase, p.Retirement.Phase = "reclaiming", "reclaiming" }, 2},
		"unknown phase":          {func(p *Prior) { p.Phase, p.Retirement.Phase = "ready", "ready" }, 2},
		"retired another phase":  {func(p *Prior) { p.Retirement.Phase = "begun" }, 2},
		"retired another intent": {func(p *Prior) { p.Retirement.IntentSHA256 = strings.Repeat("e", 64) }, 2},
		"retired another spec":   {func(p *Prior) { p.Retirement.SpecSHA256 = strings.Repeat("e", 64) }, 2},
		"no intent digest":       {func(p *Prior) { p.IntentSHA256, p.Retirement.IntentSHA256 = "", "" }, 2},
		"no spec digest":         {func(p *Prior) { p.SpecSHA256, p.Retirement.SpecSHA256 = "", "" }, 2},
		"another operation":      {func(p *Prior) { p.Retirement.Operation = `{"OperationID":"x"}` }, 2},
		"no operation":           {func(p *Prior) { p.Operation, p.Retirement.Operation = "", "" }, 2},
		"another policy":         {func(p *Prior) { p.Retirement.PolicyDigest = strings.Repeat("e", 64) }, 2},
		"no policy":              {func(p *Prior) { p.PolicyDigest, p.Retirement.PolicyDigest = "", "" }, 2},
		"retired another bench":  {func(p *Prior) { p.Retirement.ID = strings.Repeat("9", 32) }, 2},
		"retired another key":    {func(p *Prior) { p.Retirement.Key = "other" }, 2},
		"approved another key":   {func(p *Prior) { p.Retirement.ApprovalKey = "other" }, 2},
		"operation not denied":   {func(p *Prior) { p.Retirement.OperationDenied = false }, 2},
		"retired under another":  {func(p *Prior) { p.Retirement.Authority = strings.Repeat("b", 64) }, 2},
		"created under another":  {func(p *Prior) { p.Authority = strings.Repeat("b", 64) }, 2},
		"inspected another":      {func(p *Prior) { other(&p.Retirement.Recorded) }, 2},
		"approved another":       {func(p *Prior) { other(&p.Retirement.Approved) }, 2},
		"approved other caps":    {func(p *Prior) { p.Retirement.Approved.Bytes *= 2 }, 2},
		"recorded another":       {func(p *Prior) { p.ProjectID = 10005 }, 2},
		"recorded another leaf":  {func(p *Prior) { p.Path += "x" }, 2},
		"another bench's leaf":   {func(p *Prior) { p.Path += "x"; p.Retirement.Recorded.Path, p.Retirement.Approved.Path = p.Path, p.Path }, 2},
		"a shared project": {func(p *Prior) {
			p.ProjectID, p.Retirement.Recorded.ProjectID, p.Retirement.Approved.ProjectID = 101, 101, 101
		}, 2},
		"reserved with a project":  {func(p *Prior) { reserve(p); p.ProjectID, p.Retirement.Recorded.ProjectID = 10004, 10004 }, 2},
		"reserved, inspected one":  {func(p *Prior) { reserve(p); p.Retirement.Recorded.ProjectID = 10004 }, 2},
		"reserved, shared project": {func(p *Prior) { reserve(p); p.Retirement.Approved.ProjectID = 101 }, 2},
		"another workspace":        {func(p *Prior) { p.ID, p.Retirement.ID = strings.Repeat("9", 32), strings.Repeat("9", 32) }, 2},
		"another key":              {func(p *Prior) { p.Key, p.Retirement.Key, p.Retirement.ApprovalKey = "other", "other", "other" }, 2},
		"skips one":                {func(*Prior) {}, 3},
		"first generation":         {func(*Prior) {}, 0},
	} {
		p, _, _ := retired()
		c.change(&p)
		if err := Follows(p, "card", prior.ID, c.generation, authority, root); !errors.Is(err, ErrGeneration) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The reclaimed path is unchanged and needs no retirement.
	reclaimed := Prior{Key: "card", ID: prior.ID, Phase: "reclaimed", Authority: authority, Path: root, ProjectID: 10000}
	if err := Retired(reclaimed, authority, root); !errors.Is(err, ErrGeneration) {
		t.Fatalf("a reclaimed generation counted as retired: %v", err)
	}
	t.Log("FAILED_CREATE_UNRETIRED_PREDECESSOR_REFUSED_PASS")
}
