package protocolv2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestPinnedWorkspaceResponseSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/response-source.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Repository, Revision string
		Files                map[string]struct{ Local, SHA256 string }
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	if source.Repository != "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty" || source.Revision != "63af0f8675ef2a35188465884c4416a190f6f41f" {
		t.Fatal("canonical response source differs")
	}
	want := map[string]struct{ local, hash string }{
		"schemas/superbench/response.v2.json":              {"response.v2.json", "31883310fd7fe8bad85a63d6faa5fc2aabe71d3a84c3bd0fc14311c92f9f3360"},
		"fixtures/superbench-api/workspace-responses.json": {"workspace-responses.json", "296f36452e2418c805c625f97cf96da5f5028aadf2e035034df7fc8913b320b3"},
		"fixtures/superbench-api/workspace-lifecycle.json": {"workspace-lifecycle.json", "0d0dca060902df3706bf3d0c1322c1ea1fbfb22ac5cb50a3243e761906848255"},
	}
	if len(source.Files) != len(want) {
		t.Fatal("response source file list differs")
	}
	for canonical, expected := range want {
		entry, ok := source.Files[canonical]
		if !ok || entry.Local != expected.local || entry.SHA256 != expected.hash {
			t.Fatal("response source entry differs", canonical)
		}
		raw, err := os.ReadFile("testdata/" + expected.local)
		sum := sha256.Sum256(raw)
		if err != nil || hex.EncodeToString(sum[:]) != expected.hash {
			t.Fatal("canonical response bytes differ", canonical, err)
		}
	}
}
