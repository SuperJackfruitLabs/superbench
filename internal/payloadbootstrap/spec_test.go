package payloadbootstrap

import "strings"

// validSpecForTest is a first-generation spec; the codec tests are in
// internal/payloadbootstraptest.
func validSpecForTest() Spec {
	return Spec{Version: Version, Key: "card", CheckpointRepository: "api", ID: strings.Repeat("a", 32), Members: []Member{{Name: "api", Repository: "https://example.invalid/repo.git", Branch: "card/task", Source: strings.Repeat("b", 40), ObjectFormat: "sha1"}}}
}
