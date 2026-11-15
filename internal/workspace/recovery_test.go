package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statestore"
)

func recoveryFixture(t *testing.T) (*stagingFixture, Lifecycle, *model.Bench, Manager, RecoverySpec) {
	t.Helper()
	f, l, source := availableLifecycle(t)
	spec := RecoverySpec{Key: "recovered", QuotaBytes: 32 << 20, Source: RecoverySource{Repository: source.Workspace.Members[0].Repository, BenchID: source.Workspace.ID, Commit: source.Workspace.Checkpoint.Seal, ManifestSHA256: source.Workspace.Checkpoint.Digest}}
	for _, member := range source.Workspace.Members {
		spec.Repositories = append(spec.Repositories, Repository{Name: member.Name, Repository: member.Repository})
	}
	manager := f.manager
	manager.Allocate = func(b *model.Bench) error {
		f.allocations++
		stored := recoveryRecord(t, f, spec.Key)
		if !f.held || stored == nil || stored.Lease == nil || !reflect.DeepEqual(stored.Workspace.RecoveredFrom, &spec.Source) || len(stored.Workspace.Members) != 2 {
			t.Fatal("recovery allocation preceded durable provenance/vector/owner")
		}
		b.ProjectID = 10002
		b.QuotaBytes = 32 << 20
		b.QuotaInodes = 1000
		return nil
	}
	return f, l, source, manager, spec
}

func recoveryRecord(t *testing.T, f *stagingFixture, key string) *model.Bench {
	t.Helper()
	db, err := statestore.Open(filepath.Join(f.manager.State, "bench.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := benchstate.Load(db)
	if err != nil {
		t.Fatal(err)
	}
	return rows[key]
}

func TestRecoveryOlderSealAfterPartialPublicationCreatesIndependentGenesis(t *testing.T) {
	f, l, source, manager, spec := recoveryFixture(t)
	owned, err := l.Acquire(context.Background(), source.Key, "run:original")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range owned.Workspace.Members {
		git(t, member.Path, "commit", "--allow-empty", "-m", "new unpublished generation")
	}
	hook := rejectTask(t, owned.Workspace.Members[1])
	partial, err := l.Checkpoint(context.Background(), owned.Key, owned.Lease.Token)
	if err == nil || partial.Workspace.Pending == nil {
		t.Fatal("expected partial publication", err)
	}
	firstTip := remoteTask(t, partial.Workspace.Members[0])
	if firstTip == source.Workspace.Checkpoint.Members[0].Commit {
		t.Fatal("fixture did not advance first remote task")
	}
	recovered, err := manager.StageRecovery(context.Background(), "recover1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Workspace.Phase != "initializing" || recovered.Workspace.ID == source.Workspace.ID || recovered.ProjectID == source.ProjectID || recovered.Lease.Token == owned.Lease.Token || recovered.Workspace.Checkpoint != nil || recovered.Workspace.Pending != nil || !reflect.DeepEqual(recovered.Workspace.RecoveredFrom, &spec.Source) {
		t.Fatal("recovery inherited source identity/authority/checkpoint")
	}
	for i, member := range recovered.Workspace.Members {
		if member.Source != source.Workspace.Checkpoint.Members[i].Commit || git(t, member.Path, "rev-parse", "HEAD") != member.Source || member.Branch != "bench/recovered" || member.RequestedQuotaBytes != 0 {
			t.Fatal("recovery selected current tips instead of sealed sources")
		}
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := manager.Within(func(update Update) error {
		return update(func(rows benchstate.Database, _ *sql.Tx) error {
			b := rows[spec.Key]
			b.Workspace.Phase = "prepared"
			for _, member := range b.Workspace.Members {
				b.Workspace.Preparation = append(b.Workspace.Preparation, model.WorkspacePreparation{Name: member.Name, Digest: strings.Repeat("a", 64)})
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	ready, err := l.FinishInitialization(context.Background(), spec.Key, recovered.Lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Lease != nil || ready.Workspace.Phase != "ready" || ready.Workspace.Checkpoint.Previous != "" || ready.Workspace.Checkpoint.Seal == spec.Source.Commit {
		t.Fatal("new workspace did not create its own genesis")
	}
	if !reflect.DeepEqual(f.read(t), partial) || remoteTask(t, partial.Workspace.Members[0]) != firstTip {
		t.Fatal("recovery changed original writer or partial task refs")
	}
	mirrors := map[string]string{}
	for _, member := range ready.Workspace.Members {
		mirrors[member.Repository] = member.Mirror
	}
	if _, err := (checkpointset.Coordinator{Mirrors: mirrors}).Recover(context.Background(), spec.Source.Repository, spec.Source.BenchID, spec.Source.Commit, spec.Source.ManifestSHA256); err != nil {
		t.Fatal("recovery damaged original remote seal", err)
	}
	replay, err := manager.StageRecovery(context.Background(), "recover1", spec)
	if err != nil || !reflect.DeepEqual(replay, ready) {
		t.Fatal("completed recovery replay changed genesis", err)
	}
}

func TestRecoveryPartialMaterializationRetryUsesDurableSourcesWithoutRemotes(t *testing.T) {
	f, _, source, manager, spec := recoveryFixture(t)
	realGit := manager.Git
	failed := false
	manager.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		if len(args) > 1 && args[0] == "worktree" && args[1] == "add" && dir == source.Workspace.Members[1].Mirror && !failed {
			failed = true
			return "", errors.New("second recovery checkout failed")
		}
		return realGit(ctx, dir, args...)
	}
	partial, err := manager.StageRecovery(context.Background(), "recover1", spec)
	if err == nil || !failed || partial == nil || partial.Workspace.Phase != "materializing" {
		t.Fatal(partial, err)
	}
	stored := recoveryRecord(t, f, spec.Key)
	if !reflect.DeepEqual(partial, stored) {
		t.Fatal("recovery returned uncommitted state")
	}
	partial.Workspace.RecoveredFrom.Commit = strings.Repeat("f", 40)
	partial.Workspace.Members[0].Source = strings.Repeat("e", 40)
	partial.Lease.Token = "caller-mutated"
	for _, member := range source.Workspace.Members {
		if err := os.Rename(member.Repository, member.Repository+"-preserved"); err != nil {
			t.Fatal(err)
		}
	}
	manager.Git = realGit
	resumed, err := manager.StageRecovery(context.Background(), "recover1", spec)
	if err != nil || resumed.Workspace.Phase != "initializing" || resumed.Workspace.ID != stored.Workspace.ID || resumed.Lease.Token != stored.Lease.Token || !reflect.DeepEqual(resumed.Workspace.RecoveredFrom, stored.Workspace.RecoveredFrom) {
		t.Fatal("retry reselected sources or replaced private owner", err)
	}
	for i, member := range resumed.Workspace.Members {
		if member.Source != stored.Workspace.Members[i].Source || git(t, member.Path, "rev-parse", "HEAD") != member.Source {
			t.Fatal("retry lost frozen recovery vector")
		}
	}
	ordinary := Spec{Key: spec.Key, CheckpointRepository: "a", Repositories: spec.Repositories, QuotaBytes: spec.QuotaBytes}
	if _, err := manager.Stage(context.Background(), "recover1", ordinary); err == nil {
		t.Fatal("ordinary creation adopted recovered identity")
	}
	changed := spec
	changed.Source.ManifestSHA256 = strings.Repeat("f", 64)
	if _, err := manager.StageRecovery(context.Background(), "recover1", changed); err == nil {
		t.Fatal("replay rebound recovery source")
	}
	if !reflect.DeepEqual(f.read(t), source) {
		t.Fatal("retry changed original workspace")
	}
}

func TestRecoveryRefusesWrongAllowlistSealAndOriginalTaskNamespaceBeforeAllocation(t *testing.T) {
	f, _, source, manager, spec := recoveryFixture(t)
	start := f.allocations
	cases := map[string]func(*RecoverySpec){
		"digest":          func(s *RecoverySpec) { s.Source.ManifestSHA256 = strings.Repeat("f", 64) },
		"seal":            func(s *RecoverySpec) { s.Source.Commit = strings.Repeat("f", 40) },
		"missing-member":  func(s *RecoverySpec) { s.Repositories = s.Repositories[:1] },
		"renamed-member":  func(s *RecoverySpec) { s.Repositories[1].Name = "other" },
		"original-task":   func(s *RecoverySpec) { s.Repositories[0].Branch = source.Workspace.Members[0].Branch },
		"original-child":  func(s *RecoverySpec) { s.Repositories[0].Branch = source.Workspace.Members[0].Branch + "/child" },
		"original-parent": func(s *RecoverySpec) { s.Repositories[0].Branch = "bench" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			bad := spec
			bad.Repositories = append([]Repository(nil), spec.Repositories...)
			change(&bad)
			if b, err := manager.StageRecovery(context.Background(), "recover1", bad); err == nil || b != nil {
				t.Fatal("invalid recovery accepted", b, err)
			}
			if recoveryRecord(t, f, spec.Key) != nil || f.allocations != start {
				t.Fatal("invalid recovery allocated or registered state")
			}
			if _, err := os.Lstat(filepath.Join(manager.Data, "benches", spec.Key)); !os.IsNotExist(err) {
				t.Fatal("invalid recovery created checkout", err)
			}
		})
	}
	if !reflect.DeepEqual(source, f.read(t)) {
		t.Fatal("rejected recovery changed source")
	}
}

func TestRecoveryPureValidationAndCollisionPreflight(t *testing.T) {
	f, _, _, manager, spec := recoveryFixture(t)
	for _, change := range []func(*RecoverySpec){
		func(s *RecoverySpec) { s.Repositories[0].Base = "main" },
		func(s *RecoverySpec) { s.Source.Repository = "/not-allowed" },
		func(s *RecoverySpec) { s.Source.BenchID = "invalid" },
		func(s *RecoverySpec) { s.Source.ManifestSHA256 = "invalid" },
		func(s *RecoverySpec) { s.Source.Commit = "invalid" },
	} {
		bad := spec
		bad.Repositories = append([]Repository(nil), spec.Repositories...)
		change(&bad)
		noEffects := manager
		noEffects.Within = func(func(Update) error) error { t.Fatal("invalid recovery reached state"); return nil }
		if _, err := noEffects.StageRecovery(context.Background(), "recover1", bad); err == nil {
			t.Fatal("invalid recovery passed pure validation")
		}
	}
	manager.Admit = func() error { t.Fatal("collision reached source/allocation admission"); return nil }
	for _, path := range []string{filepath.Join(manager.Data, "benches", spec.Key), filepath.Join(manager.State, "create-"+spec.Key+".json")} {
		if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.StageRecovery(context.Background(), "recover1", spec); err == nil {
			t.Fatal("unregistered collision adopted")
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "preserve" {
			t.Fatal("collision was modified", err)
		}
		if err := os.Rename(path, path+"-preserved"); err != nil {
			t.Fatal(err)
		}
	}
	if recoveryRecord(t, f, spec.Key) != nil {
		t.Fatal("collision created recovery state")
	}
}

func TestRecoveryMissingOrChangedPinCannotCreateIntent(t *testing.T) {
	for _, kind := range []string{"missing", "changed"} {
		t.Run(kind, func(t *testing.T) {
			f, _, source, manager, spec := recoveryFixture(t)
			member := source.Workspace.Members[1]
			pin, err := checkpointset.PinRef(source.Workspace.ID, source.Workspace.Checkpoint.GenerationID, member.Name)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "missing" {
				git(t, member.Repository, "update-ref", "-d", pin)
			} else {
				git(t, member.Repository, "commit", "--allow-empty", "-m", "foreign pin target")
				git(t, member.Repository, "update-ref", pin, git(t, member.Repository, "rev-parse", "HEAD"))
			}
			manager.Allocate = func(*model.Bench) error { t.Fatal("unconfirmed pin reached quota"); return nil }
			if b, err := manager.StageRecovery(context.Background(), "recover1", spec); err == nil || b != nil {
				t.Fatal("invalid immutable pin accepted", err)
			}
			if recoveryRecord(t, f, spec.Key) != nil || !reflect.DeepEqual(source, f.read(t)) {
				t.Fatal("invalid pin created intent or changed source owner")
			}
		})
	}
}

func TestRecoveryQuotaUsesSealedCommitInsteadOfNewerConfiguration(t *testing.T) {
	f, _, source, manager, spec := recoveryFixture(t)
	for _, member := range source.Workspace.Members {
		if err := os.Mkdir(filepath.Join(member.Repository, ".bench"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(member.Repository, ".bench", "config.toml"), []byte("[quota]\nsize = \"1GiB\"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		git(t, member.Repository, "add", ".bench/config.toml")
		git(t, member.Repository, "commit", "-m", "new quota after sealed generation")
	}
	recovered, err := manager.StageRecovery(context.Background(), "recover1", spec)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.RequestedQuotaBytes != spec.QuotaBytes || recovered.QuotaBytes != spec.QuotaBytes {
		t.Fatal("newer configuration changed recovered total budget")
	}
	for i, member := range recovered.Workspace.Members {
		if member.RequestedQuotaBytes != 0 || member.Source != source.Workspace.Checkpoint.Members[i].Commit {
			t.Fatal("recovery read quota outside the exact sealed vector")
		}
	}
	if !reflect.DeepEqual(source, f.read(t)) {
		t.Fatal("quota planning changed source")
	}
}

func TestRecoveryAllocationSaveFailureKeepsPinnedIntentBeforeCheckout(t *testing.T) {
	f, _, source, manager, spec := recoveryFixture(t)
	allocate := manager.Allocate
	manager.Allocate = func(b *model.Bench) error {
		if err := allocate(b); err != nil {
			return err
		}
		f.failSave = true
		return nil
	}
	partial, err := manager.StageRecovery(context.Background(), "recover1", spec)
	if err == nil || partial == nil || partial.ProjectID != 0 || partial.Workspace.Phase != "materializing" || partial.Lease == nil || !reflect.DeepEqual(partial.Workspace.RecoveredFrom, &spec.Source) {
		t.Fatal("failed allocation save lost pinned intent", err)
	}
	if !reflect.DeepEqual(partial, recoveryRecord(t, f, spec.Key)) {
		t.Fatal("failure returned non-durable allocation")
	}
	for _, member := range partial.Workspace.Members {
		if _, err := os.Lstat(member.Path); !os.IsNotExist(err) {
			t.Fatal("checkout preceded durable quota assignment", err)
		}
	}
	manager.Allocate = allocate
	resumed, err := manager.StageRecovery(context.Background(), "recover1", spec)
	if err != nil || resumed.Workspace.ID != partial.Workspace.ID || resumed.Lease.Token != partial.Lease.Token || resumed.ProjectID != 10002 {
		t.Fatal("allocation retry changed recovery identity/owner", err)
	}
	if !reflect.DeepEqual(source, f.read(t)) {
		t.Fatal("allocation failure changed original workspace")
	}
}
