package checkpointset

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestPinnedCheckpointSources(t *testing.T) {
	raw, err := os.ReadFile("testdata/source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
		Files      map[string]struct {
			Local  string `json:"local"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err = json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.Repository != "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(source.Revision) {
		t.Fatal("missing canonical checkpoint source identity")
	}
	expected := map[string]string{
		"schemas/superbench/checkpoint.v1.json":    "checkpoint.v1.json",
		"fixtures/superbench-api/checkpoints.json": "checkpoints.json",
	}
	if len(source.Files) != len(expected) {
		t.Fatal("checkpoint source manifest differs")
	}
	for canonical, local := range expected {
		entry, ok := source.Files[canonical]
		if !ok || entry.Local != local {
			t.Fatal("checkpoint source path differs", canonical)
		}
		raw, err = os.ReadFile("testdata/" + local)
		if err != nil || Digest(raw) != entry.SHA256 {
			t.Fatal("pinned checkpoint bytes changed", local, err)
		}
	}
}
