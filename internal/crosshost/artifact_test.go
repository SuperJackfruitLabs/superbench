package crosshost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func fixture() Artifact {
	return Artifact{Version: Version,
		Source:               Source{HostID: strings.Repeat("a", 64), StateID: strings.Repeat("b", 32), WorkspaceID: strings.Repeat("c", 32), Key: "original", TransferID: "move-1", ApprovalDigest: strings.Repeat("d", 64)},
		Destination:          Destination{HostID: strings.Repeat("e", 64), StateID: strings.Repeat("f", 32), WorkspaceID: strings.Repeat("1", 32), Key: "restored", CreationID: "create-1"},
		CheckpointRepository: "app", Seal: strings.Repeat("2", 40), ManifestDigest: strings.Repeat("3", 64),
		Members: []Member{{Name: "app", Repository: "https://forge.example/app.git", Branch: "card/card_example01", IntegrationBase: "main", Commit: strings.Repeat("4", 40)},
			{Name: "library", Repository: "https://forge.example/library.git", Branch: "card/card_example01", IntegrationBase: "main", Commit: strings.Repeat("5", 64)}}}
}

func TestExactRoundTripAndDestination(t *testing.T) {
	a := fixture()
	raw, err := Seal(a)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Digest == "" || parsed.Members[0].Branch != a.Members[0].Branch {
		t.Fatal("lost original-ref binding")
	}
	if err = parsed.RequireDestination(a.Destination); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"host", "state", "workspace", "key", "creation"} {
		t.Run(field, func(t *testing.T) {
			actual := a.Destination
			switch field {
			case "host":
				actual.HostID = strings.Repeat("9", 64)
			case "state":
				actual.StateID = strings.Repeat("9", 32)
			case "workspace":
				actual.WorkspaceID = strings.Repeat("9", 32)
			case "key":
				actual.Key = "other"
			case "creation":
				actual.CreationID = "other"
			}
			if parsed.RequireDestination(actual) == nil {
				t.Fatal("accepted another destination")
			}
		})
	}
	if bytes.Contains(raw, []byte("Token")) || bytes.Contains(raw, []byte("RootPath")) {
		t.Fatal("capability/path in handoff")
	}
}

func TestClosedArtifactAndTampering(t *testing.T) {
	raw, err := Seal(fixture())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"trailing":    append(append([]byte{}, raw...), []byte("{}")...),
		"unknown":     bytes.Replace(raw, []byte(`"Version":`), []byte(`"fenced":true,"Version":`), 1),
		"duplicate":   bytes.Replace(raw, []byte(`"Version":`), []byte(`"Version":"`+Version+`","Version":`), 1),
		"changed-ref": bytes.Replace(raw, []byte("card/card_example01"), []byte("card/card_other0001"), 1),
		"encoding":    append([]byte{0xff}, raw...),
		"oversized":   bytes.Repeat([]byte(" "), MaxArtifact+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(body); err == nil {
				t.Fatal("accepted changed or open artifact")
			}
		})
	}
}

func TestInvalidVectorsAndAuthorities(t *testing.T) {
	for _, kind := range []string{"same-host", "same-state", "same-workspace", "missing-source-approval", "no-coordinator", "duplicate-member", "unsorted", "bad-branch", "bad-base", "bad-commit", "empty-repository", "control-repository", "other-control", "oversize-repository", "same-repository-ref", "repository-ref-prefix", "too-many"} {
		t.Run(kind, func(t *testing.T) {
			a := fixture()
			switch kind {
			case "same-host":
				a.Destination.HostID = a.Source.HostID
			case "same-state":
				a.Destination.StateID = a.Source.StateID
			case "same-workspace":
				a.Destination.WorkspaceID = a.Source.WorkspaceID
			case "missing-source-approval":
				a.Source.ApprovalDigest = ""
			case "no-coordinator":
				a.CheckpointRepository = "other"
			case "duplicate-member":
				a.Members[1] = a.Members[0]
			case "unsorted":
				a.Members[0], a.Members[1] = a.Members[1], a.Members[0]
			case "bad-branch":
				a.Members[0].Branch = "../main"
			case "bad-base":
				a.Members[0].IntegrationBase = "main.lock"
			case "bad-commit":
				a.Members[0].Commit = strings.Repeat("A", 40)
			case "empty-repository":
				a.Members[0].Repository = ""
			case "control-repository":
				a.Members[0].Repository = "/repo\nother"
			case "other-control":
				a.Members[0].Repository = "/repo\x01other"
			case "same-repository-ref":
				a.Members[1].Repository = a.Members[0].Repository
			case "repository-ref-prefix":
				a.Members[1].Repository = a.Members[0].Repository
				a.Members[1].Branch = a.Members[0].Branch + "/nested"
			case "oversize-repository":
				a.Members[0].Repository = strings.Repeat("x", 8193)
			case "too-many":
				for len(a.Members) < 33 {
					a.Members = append(a.Members, a.Members[0])
				}
			}
			if _, err := Seal(a); err == nil {
				t.Fatal("accepted invalid handoff")
			}
		})
	}
}

func TestResealedOtherDestinationCannotBeAdopted(t *testing.T) {
	a := fixture()
	actual := a.Destination
	a.Destination.HostID = strings.Repeat("9", 64)
	raw, err := Seal(a)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RequireDestination(actual) == nil {
		t.Fatal("a valid digest granted another host authority")
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 8 {
		t.Fatalf("unexpected public fields: %d", len(wire))
	}
}

func TestFullVectorCannotExceedArtifactBound(t *testing.T) {
	a := fixture()
	a.CheckpointRepository = "m00"
	a.Members = nil
	for i := 0; i < 32; i++ {
		a.Members = append(a.Members, Member{Name: fmt.Sprintf("m%02d", i), Repository: strings.Repeat("x", 8188) + fmt.Sprintf("%04d", i), Branch: "card/card_example01", IntegrationBase: "main", Commit: strings.Repeat("4", 40)})
	}
	if _, err := Seal(a); err == nil || !strings.Contains(err.Error(), "exceeds its bound") {
		t.Fatalf("expected whole-artifact size refusal, got %v", err)
	}
}
