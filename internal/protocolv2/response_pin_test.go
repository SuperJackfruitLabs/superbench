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
	if source.Repository != "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty" || source.Revision != "83e51d583a71f668eec8c2093394613e002c1678" {
		t.Fatal("canonical response source differs")
	}
	want := map[string]struct{ local, hash string }{
		"schemas/superbench/response.v2.json":              {"response.v2.json", "64e72583a3e75da94534e28985d01ab9ac12b5bfbc25c37487f975e93a8a0053"},
		"fixtures/superbench-api/workspace-responses.json": {"workspace-responses.json", "1c82207d680f420b49b66741374be29b512093cfe70d5106f108f2c6ebad06f6"},
		"fixtures/superbench-api/reclaim.json":             {"reclaim.json", "f18828e44ea42cbae7875906f5472633a8c291feff062e87e58dd526b7371084"},
		"fixtures/superbench-api/reclaim-lifecycle.json":   {"reclaim-lifecycle.json", "19fd90285c4e465b95e0e455ff4d4071b022baf5b1ef066858923d7fab064a52"},
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
