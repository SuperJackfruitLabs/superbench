package mirrorstage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCapturedDirectoriesRefuseRenamedPathsWithoutRedirectingWrites(t *testing.T) {
	for _, kind := range []string{"state", "journal", "parent"} {
		t.Run(kind, func(t *testing.T) {
			state, parent, _ := fixture(t)
			s, e := bind(state)
			if e != nil {
				t.Fatal(e)
			}
			defer s.close()
			j, e := openJournal(s, true)
			if e != nil {
				t.Fatal(e)
			}
			defer j.close()
			p, e := bind(parent)
			if e != nil {
				t.Fatal(e)
			}
			defer p.close()
			changed := map[string]*boundDir{"state": s, "journal": j, "parent": p}[kind]
			if e = os.Rename(changed.path, changed.path+"-retained"); e != nil {
				t.Fatal(e)
			}
			if e = os.Mkdir(changed.path, 0700); e != nil {
				t.Fatal(e)
			}
			sentinel := filepath.Join(changed.path, "unknown")
			if e = os.WriteFile(sentinel, []byte("preserve replacement"), 0600); e != nil {
				t.Fatal(e)
			}
			id := strings.Repeat("b", 32)
			plan := &Intent{Version: Version, ID: id, State: state, Parent: parent, Mirror: filepath.Join(parent, strings.Repeat("a", 64)+".git"), Stage: filepath.Join(parent, ".clone-"+id), CreatedAt: time.Now().UTC(), ParentIdentity: p.id}
			if e = createStage(plan, s, j, p); e == nil {
				t.Fatal("created stage after directory replacement")
			}
			if e = saveBound(changed, "new.json", plan); e == nil {
				t.Fatal("redirected journal write")
			}
			for _, dir := range []string{changed.path, changed.path + "-retained"} {
				if _, e = os.Lstat(filepath.Join(dir, "new.json")); !os.IsNotExist(e) {
					t.Fatal("wrote through changed path", dir, e)
				}
			}
			raw, e := os.ReadFile(sentinel)
			if e != nil || string(raw) != "preserve replacement" {
				t.Fatal("changed replacement bytes", e)
			}
			entries, e := p.file.ReadDir(-1)
			if e != nil {
				t.Fatal(e)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".clone-") {
					t.Fatal("stage created after anchor changed")
				}
			}
		})
	}
}

func TestRootRecordConflictPreservesIntentStageAndForeignRecord(t *testing.T) {
	state, parent, mirror := fixture(t)
	s, e := bind(state)
	if e != nil {
		t.Fatal(e)
	}
	defer s.close()
	j, e := openJournal(s, true)
	if e != nil {
		t.Fatal(e)
	}
	defer j.close()
	p, e := bind(parent)
	if e != nil {
		t.Fatal(e)
	}
	defer p.close()
	id := strings.Repeat("c", 32)
	intent := &Intent{Version: Version, ID: id, State: state, Parent: parent, Mirror: mirror, Stage: filepath.Join(parent, ".clone-"+id), ParentIdentity: p.id, CreatedAt: time.Now().UTC()}
	path := filepath.Join(j.path, id+"-root.json")
	if e = os.WriteFile(path, []byte("foreign record"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = createStage(intent, s, j, p); e == nil {
		t.Fatal("replaced existing root authority")
	}
	if _, e = os.Stat(intent.Stage); e != nil {
		t.Fatal("removed failed stage", e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || string(raw) != "foreign record" {
		t.Fatal("lost foreign record", e)
	}
	var saved Intent
	if e = readRecord(j.path, id+".json", &saved); e != nil || saved != *intent {
		t.Fatal("lost durable intent", e)
	}
	if _, _, e = Load(state, parent, id); e == nil {
		t.Fatal("adopted failed root record")
	}
}

func TestBudgetReservesBothFilesAndCountsUnknownEntries(t *testing.T) {
	for _, slots := range []int{1, 2} {
		t.Run(string(rune('0'+slots)), func(t *testing.T) {
			state, parent, mirror := fixture(t)
			dir, e := journal(state, true)
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < MaxJournalEntries-slots; i++ {
				// Deliberately unfamiliar names must consume the full admission budget.
				name := filepath.Join(dir, time.Unix(int64(i), 0).UTC().Format("150405"))
				if e = os.WriteFile(name, []byte("unknown"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			_, e = Create(state, parent, mirror)
			if slots == 1 && e == nil {
				t.Fatal("exceeded hard journal capacity")
			}
			if slots == 2 && e != nil {
				t.Fatal(e)
			}
			entries, e := os.ReadDir(dir)
			if e != nil {
				t.Fatal(e)
			}
			want := MaxJournalEntries
			if slots == 1 {
				want--
			}
			if len(entries) != want {
				t.Fatal("journal entries", len(entries), "want", want)
			}
			if slots == 1 {
				entries, e = os.ReadDir(parent)
				if e != nil || len(entries) != 0 {
					t.Fatal("budget refusal created staging data", e)
				}
			}
		})
	}
}

func TestLoadRejectsTraversalAndReplacedLeafPreservesFiles(t *testing.T) {
	state, parent, mirror := fixture(t)
	p, e := Create(state, parent, mirror)
	if e != nil {
		t.Fatal(e)
	}
	_, root, e := Load(state, parent, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = CheckRoot(*p, *root); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(state, "mirror-stages", p.ID+".json")
	altered := *p
	altered.Mirror = parent + "/link/../" + filepath.Base(mirror)
	raw, e := json.Marshal(altered)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = Load(state, parent, p.ID); e == nil {
		t.Fatal("accepted noncanonical publication target")
	}
	if e = os.Rename(p.Stage, p.Stage+"-retained"); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(p.Stage, 0700); e != nil {
		t.Fatal(e)
	}
	sentinel := filepath.Join(p.Stage, "unknown")
	if e = os.WriteFile(sentinel, []byte("new work"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = CheckRoot(*p, *root); e == nil {
		t.Fatal("accepted replacement stage")
	}
	if raw, e = os.ReadFile(sentinel); e != nil || string(raw) != "new work" {
		t.Fatal("changed replacement stage", e)
	}
	if _, e = os.Stat(p.Stage + "-retained"); e != nil {
		t.Fatal("lost original stage", e)
	}
}

func TestLoadRejectsNestedStateEvenWithPlausibleRecords(t *testing.T) {
	_, parent, mirror := fixture(t)
	state := filepath.Join(parent, "state")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	dir, e := journal(state, true)
	if e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("d", 32)
	parentID, e := identity(parent)
	if e != nil {
		t.Fatal(e)
	}
	p := Intent{Version: Version, ID: id, State: state, Parent: parent, Mirror: mirror, Stage: filepath.Join(parent, ".clone-"+id), ParentIdentity: parentID, CreatedAt: time.Now().UTC()}
	if e = os.Mkdir(p.Stage, 0700); e != nil {
		t.Fatal(e)
	}
	leaf, e := identity(p.Stage)
	if e != nil {
		t.Fatal(e)
	}
	if e = save(dir, id+".json", p); e != nil {
		t.Fatal(e)
	}
	if e = save(dir, id+"-root.json", Root{ID: id, Identity: leaf}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = Load(state, parent, id); e == nil {
		t.Fatal("accepted state within collectable data")
	}
}
