package childlog

import "testing"

func TestSkippedMatchesOnlyGoSkipLines(t *testing.T) {
	for log, want := range map[string]bool{
		"=== RUN   TestFreshRecoveryFixture\n--- SKIP: TestFreshRecoveryFixture (0.00s)\n":               true,
		"=== RUN   TestA/sub\n    --- SKIP: TestA/sub (0.00s)\n--- PASS: TestA (0.01s)\n":                true,
		"    recreate_guards_linux.go:51: LOWER_RECREATE_SKIPPED_GENERATION_REFUSED_PASS\n--- PASS: T\n": false,
		"fixture.go:9: SKIP this note\nSKIP\n--- PASS: TestFreshRecoveryFixture (1.00s)\n":               false,
		"x --- SKIP: not at the start of a line\n":                                                       false,
		"": false,
	} {
		if got := Skipped([]byte(log)); got != want {
			t.Errorf("Skipped(%q) = %v, want %v", log, got, want)
		}
	}
}
