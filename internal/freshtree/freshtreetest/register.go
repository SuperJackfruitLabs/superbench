// Package freshtreetest writes a payload layout registration for tests of
// Fresh refusals. It has no production importer.
package freshtreetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// Register marks b as a registered Fresh workspace in state. Without an
// installed helper route, every Fresh selection then refuses.
func Register(t *testing.T, state string, b *model.Bench) {
	t.Helper()
	dir := filepath.Join(state, "payload-layouts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{"Version": "superbench.payload-layout.v1", "Key": b.Key, "ID": b.Workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, b.Key+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Bench is a minimal registered workspace record under data.
func Bench(data string) *model.Bench {
	return &model.Bench{Key: "card", Path: filepath.Join(data, "benches", "card"), Workspace: &model.Workspace{ID: "0123456789abcdef0123456789abcdef", Phase: "ready"}}
}
