package workspacebootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacecopy"
)

func durableFixture() (string, *model.Bench) {
	data := "/srv/superbench-data"
	root := filepath.Join(data, "benches", "card")
	repo := "https://example.invalid/repo.git"
	return data, &model.Bench{Key: "card", Path: root, RequestedQuotaBytes: 8 << 20, Lease: &model.Lease{Holder: "initialization", Token: strings.Repeat("a", 32), Continuation: strings.Repeat("b", 32)}, Workspace: &model.Workspace{ID: strings.Repeat("c", 32), CreationID: "request", CheckpointRepository: "api", ExplicitQuotaBytes: 8 << 20, Phase: "materializing", Members: []model.WorkspaceMember{{Name: "api", Repository: repo, Branch: "bench/card", Base: "main", Path: filepath.Join(root, "api"), Mirror: repository.Path(data, repo), Source: strings.Repeat("d", 40), ObjectFormat: "sha1"}}}}
}

func TestSpecificationCopiesDurableWholeVectorBeforeHelper(t *testing.T) {
	data, b := durableFixture()
	spec, err := Specification(data, b)
	if err != nil {
		t.Fatal(err)
	}
	if spec.ID != b.Workspace.ID || spec.QuotaBytes != b.RequestedQuotaBytes || spec.CheckpointRepository != "api" || len(spec.Members) != 1 || spec.Members[0].Source != b.Workspace.Members[0].Source {
		t.Fatalf("partial vector: %+v", spec)
	}
	b.Workspace.Members[0].Source = strings.Repeat("f", 40)
	if spec.Members[0].Source == b.Workspace.Members[0].Source {
		t.Fatal("spec aliases durable record")
	}
}

func TestSpecificationRefusesLegacyAllocatedUnsettledOrChangedRecords(t *testing.T) {
	data, valid := durableFixture()
	for name, change := range map[string]func(*model.Bench){
		"scalar":              func(b *model.Bench) { b.Workspace = nil },
		"allocated":           func(b *model.Bench) { b.ProjectID = 10000; b.QuotaBytes = 8 << 20; b.QuotaInodes = 4096 },
		"stopped":             func(b *model.Bench) { b.Lease.StopRequested = true; b.Lease.Continuation = "" },
		"missing-owner":       func(b *model.Bench) { b.Lease = nil },
		"wrong-root":          func(b *model.Bench) { b.Path = "/tmp/foreign" },
		"changed-member-path": func(b *model.Bench) { b.Workspace.Members[0].Path = "/tmp/foreign" },
		"changed-mirror":      func(b *model.Bench) { b.Workspace.Members[0].Mirror = "/tmp/foreign" },
		"unresolved-source":   func(b *model.Bench) { b.Workspace.Members[0].Source = "HEAD" },
		"wrong-format":        func(b *model.Bench) { b.Workspace.Members[0].ObjectFormat = "sha256" },
		"initializing":        func(b *model.Bench) { b.Workspace.Phase = "initializing" },
		"duplicate-member":    func(b *model.Bench) { b.Workspace.Members = append(b.Workspace.Members, b.Workspace.Members[0]) },
		"legacy-registration": func(b *model.Bench) { b.RegistrationID = strings.Repeat("e", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			b := workspacecopy.Copy(valid)
			change(b)
			if _, err := Specification(data, b); !errors.Is(err, ErrBootstrap) {
				t.Fatalf("wrong refusal: %v", err)
			}
		})
	}
	if _, err := Specification(data, nil); !errors.Is(err, ErrBootstrap) {
		t.Fatalf("nil: %v", err)
	}
}

func TestStageRequiresControlledOperationWithoutChangingRecord(t *testing.T) {
	data, b := durableFixture()
	before := workspacecopy.Copy(b)
	if err := Stage(context.Background(), "/var/lib/superbench", data, b); !errors.Is(err, operationcontrol.ErrAuthority) {
		t.Fatalf("uncontrolled helper admission: %v", err)
	}
	if !reflect.DeepEqual(before, b) {
		t.Fatal("refused stage changed record")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New("/var/lib/superbench", data)(ctx, b); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled stage: %v", err)
	}
	if !reflect.DeepEqual(before, b) {
		t.Fatal("canceled stage changed record")
	}
}

func TestStageRejectsUnmanagedOrForeignOperationBeforeInstalledAuthority(t *testing.T) {
	data, b := durableFixture()
	before := workspacecopy.Copy(b)
	for _, tc := range []struct{ key, operation string }{{"card", "create"}, {"other", "create"}, {"card", "run"}} {
		i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1001, RequestID: "request", Digest: strings.Repeat("a", 64)}, Key: tc.key, Operation: tc.operation, Epoch: strings.Repeat("b", 32)}
		ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
		if err != nil {
			t.Fatal(err)
		}
		if err = Stage(ctx, "/var/lib/superbench", data, b); !errors.Is(err, operationcontrol.ErrAuthority) {
			t.Fatalf("%s/%s: %v", tc.key, tc.operation, err)
		}
		if !reflect.DeepEqual(before, b) {
			t.Fatal("unmanaged operation changed record")
		}
	}
}
