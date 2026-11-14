package creation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
)

func TestPendingPreservesCreationSource(t *testing.T) {
	f := Files{State: t.TempDir(), Data: t.TempDir()}
	o := model.Options{Repo: "fixture", Branch: "bench/task", Base: "main"}
	intent := Intent{Source: strings.Repeat("a", 40), Base: "main", Bench: model.Bench{
		Key: "task", Repo: o.Repo, Branch: o.Branch,
		Mirror: repository.Path(f.Data, o.Repo), Path: filepath.Join(f.Data, "benches", "task"),
	}}
	for _, source := range []string{"", intent.Source, strings.Repeat("b", 40)} {
		intent.Bench.CreatedFrom = source
		raw, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(f.JournalPath("task"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		pending, err := f.Pending("task", o, false)
		if source == "" || source == intent.Source {
			if err != nil || pending == nil || pending.Bench.CreatedFrom != source {
				t.Fatalf("matching or legacy provenance changed: %+v %v", pending, err)
			}
		} else if err == nil || pending != nil {
			t.Fatal("changed creation provenance was adopted")
		}
		preserved, err := os.ReadFile(f.JournalPath("task"))
		if err != nil || string(preserved) != string(raw) {
			t.Fatal("inspection rewrote the journal", err)
		}
	}
}
