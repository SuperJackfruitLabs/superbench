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
	if source.Repository != "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty" || source.Revision != "4a49c96ff7fe8ccfb3a832e163e086c06fb394c7" {
		t.Fatal("canonical response source differs")
	}
	want := map[string]struct{ local, hash string }{
		"schemas/superbench/response.v2.json":              {"response.v2.json", "d2327164fb744c32c6b7773739a67fb24d92821ae07b890198eff3d297185aaf"},
		"fixtures/superbench-api/workspace-responses.json": {"workspace-responses.json", "6962ec8f7e0c0865ca9d36b2b1feede92891fe88e14387ddb77d915aa9375ca2"},
		"fixtures/superbench-api/workspace-lifecycle.json": {"workspace-lifecycle.json", "74524b71743780bd4e8f1d4f646c4193ade8508e32709c32017144212880dfeb"},
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
