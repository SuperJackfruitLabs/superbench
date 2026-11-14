package archivepolicy

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

func archiveEvidenceFixture() *model.Bench {
	b := &model.Bench{
		Branch: "card/task", CreatedFrom: strings.Repeat("a", 40), Checkpoint: strings.Repeat("b", 40),
		ArchivedAt: "2030-01-01T02:00:00Z", ReapAfter: "2030-01-08T02:00:00Z",
		ArchiveFreezeDigest: strings.Repeat("c", 64), ArchiveFrozenAt: "2030-01-01T02:00:01Z",
	}
	b.ArchiveUnmerged = &model.ArchiveMergeEvidence{Checkpoint: b.Checkpoint, ConfigDigest: strings.Repeat("d", 64), TargetBranch: "refs/heads/main", TargetCommit: strings.Repeat("e", 40), ObservedAt: "2030-01-01T00:00:00Z"}
	copy := *b.ArchiveUnmerged
	copy.TargetCommit = strings.Repeat("f", 40)
	copy.ObservedAt = "2030-01-01T01:00:00Z"
	b.ArchiveMerge = &copy
	return b
}

func TestValidAutomaticArchiveEvidenceStates(t *testing.T) {
	for _, state := range []string{"observed unmerged", "freeze intent", "frozen", "sha256", "manual", "legacy manual", "unicode slash target"} {
		t.Run(state, func(t *testing.T) {
			b := archiveEvidenceFixture()
			switch state {
			case "observed unmerged":
				b.ArchiveMerge = nil
				b.ArchivedAt = ""
				b.ReapAfter = ""
				b.ArchiveFreezeDigest = ""
				b.ArchiveFrozenAt = ""
			case "freeze intent":
				b.ArchiveFrozenAt = ""
			case "sha256":
				b.CreatedFrom = strings.Repeat("a", 64)
				b.Checkpoint = strings.Repeat("b", 64)
				b.ArchiveUnmerged.Checkpoint = b.Checkpoint
				b.ArchiveMerge.Checkpoint = b.Checkpoint
				b.ArchiveUnmerged.TargetCommit = strings.Repeat("e", 64)
				b.ArchiveMerge.TargetCommit = strings.Repeat("f", 64)
			case "unicode slash target":
				b.ArchiveUnmerged.TargetBranch = "refs/heads/团队/开发"
				b.ArchiveMerge.TargetBranch = b.ArchiveUnmerged.TargetBranch
			case "manual":
				b.ArchiveMerge = nil
				b.ArchiveUnmerged = nil
			case "legacy manual":
				b.ArchiveMerge = nil
				b.ArchiveUnmerged = nil
				b.CreatedFrom = ""
			}
			if e := ValidateArchive(b); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestCorruptedAutomaticArchiveEvidenceIsRejected(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(*model.Bench)
	}{
		{"missing prior observation", func(b *model.Bench) { b.ArchiveUnmerged = nil }},
		{"missing source", func(b *model.Bench) { b.CreatedFrom = "" }},
		{"malformed source", func(b *model.Bench) { b.CreatedFrom = strings.Repeat("z", 40) }},
		{"short source", func(b *model.Bench) { b.CreatedFrom = "abcd" }},
		{"unchanged source", func(b *model.Bench) { b.CreatedFrom = b.Checkpoint }},
		{"changed bench checkpoint", func(b *model.Bench) { b.Checkpoint = strings.Repeat("1", 40) }},
		{"changed observation checkpoint", func(b *model.Bench) { b.ArchiveUnmerged.Checkpoint = strings.Repeat("1", 40) }},
		{"malformed checkpoint", func(b *model.Bench) {
			b.Checkpoint = strings.Repeat("z", 40)
			b.ArchiveUnmerged.Checkpoint = b.Checkpoint
			b.ArchiveMerge.Checkpoint = b.Checkpoint
		}},
		{"changed config", func(b *model.Bench) { b.ArchiveMerge.ConfigDigest = strings.Repeat("1", 64) }},
		{"malformed config", func(b *model.Bench) { b.ArchiveMerge.ConfigDigest = strings.Repeat("z", 64) }},
		{"short config", func(b *model.Bench) { b.ArchiveMerge.ConfigDigest = "abcd" }},
		{"changed target branch", func(b *model.Bench) { b.ArchiveMerge.TargetBranch = "refs/heads/trunk" }},
		{"missing target branch", func(b *model.Bench) { b.ArchiveMerge.TargetBranch = "" }},
		{"empty head name", func(b *model.Bench) { b.ArchiveMerge.TargetBranch = "refs/heads/" }},
		{"nonhead ref", func(b *model.Bench) { b.ArchiveMerge.TargetBranch = "refs/tags/main" }},
		{"task is default", func(b *model.Bench) { b.ArchiveMerge.TargetBranch = "refs/heads/" + b.Branch }},
		{"target whitespace", func(b *model.Bench) { b.ArchiveMerge.TargetBranch = "refs/heads/main\n" }},
		{"malformed target commit", func(b *model.Bench) { b.ArchiveMerge.TargetCommit = strings.Repeat("z", 40) }},
		{"short prior target commit", func(b *model.Bench) { b.ArchiveUnmerged.TargetCommit = "abcd" }},
		{"malformed prior time", func(b *model.Bench) { b.ArchiveUnmerged.ObservedAt = "yesterday" }},
		{"zero merge time", func(b *model.Bench) { b.ArchiveMerge.ObservedAt = "0001-01-01T00:00:00Z" }},
		{"missing merge time", func(b *model.Bench) { b.ArchiveMerge.ObservedAt = "" }},
		{"prior after merge", func(b *model.Bench) { b.ArchiveUnmerged.ObservedAt = "2030-01-01T01:00:01Z" }},
		{"merge after archive", func(b *model.Bench) { b.ArchiveMerge.ObservedAt = "2030-01-01T02:00:01Z" }},
		{"no archive", func(b *model.Bench) { b.ArchivedAt = "" }},
		{"invalid archive time", func(b *model.Bench) { b.ArchivedAt = "bad" }},
		{"no freeze intent", func(b *model.Bench) { b.ArchiveFreezeDigest = "" }},
		{"invalid freeze intent", func(b *model.Bench) { b.ArchiveFreezeDigest = "bad" }},
		{"freeze predates archive", func(b *model.Bench) { b.ArchiveFrozenAt = "2030-01-01T01:59:59Z" }},
		{"invalid frozen time", func(b *model.Bench) { b.ArchiveFrozenAt = "bad" }},
		{"deletion before freeze", func(b *model.Bench) { b.ArchiveFrozenAt = ""; b.ReapingDigest = "approved" }},
	}
	for _, ref := range []string{"refs/heads/../main", "refs/heads/main..other", "refs/heads/main.lock", "refs/heads/.hidden/main", "refs/heads/main.", "refs/heads/ma^in", "refs/heads/ma\x01in", "refs/heads/teams//main"} {
		ref := ref
		cases = append(cases, struct {
			name    string
			corrupt func(*model.Bench)
		}{"invalid ref " + ref, func(b *model.Bench) {
			b.ArchiveUnmerged.TargetBranch = ref
			b.ArchiveMerge.TargetBranch = ref
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := archiveEvidenceFixture()
			tc.corrupt(b)
			if e := ValidateArchive(b); e == nil {
				t.Fatal("corrupted state accepted")
			}
		})
	}
}
