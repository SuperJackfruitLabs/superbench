// Package workspaceplan selects aggregate source vectors before allocation or
// checkout materialization. It does not grant leases or publish readiness.
package workspaceplan

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotapolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

type Repository struct {
	Name, Repository, Branch, Base string
}

type Spec struct {
	// TransferID selects an already approved, permanently relinquished source.
	// It is local authority, not a field in the public create protocol.
	TransferID                string
	Key, CheckpointRepository string
	Repositories              []Repository
	QuotaBytes                uint64
}

type Member struct {
	Name, Repository, Branch, Base string
	Path, Mirror, Source           string
	ObjectFormat                   string
	// RepositoryQuotaBytes preserves each source declaration independently of
	// the one aggregate allocation. Setup compares its own declaration to this.
	RepositoryQuotaBytes uint64
}

type Plan struct {
	Key, Root, CheckpointRepository string
	QuotaBytes                      uint64
	Members                         []Member
}

// Planner requires the engine's host-wide mirror/state lock. Git must use the
// same managed control context as Mirrors; callers must not substitute plain
// exec for supervised Git. All sources are selected before any checkout exists.
type Planner struct {
	Mirrors repository.Mirrors
	Git     func(context.Context, string, ...string) (string, error)
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

// Normalize validates a request and supplies ordinary branch/base defaults.
// TransferID is preserved for journal-authorized staging, not interpreted here.
func Normalize(s Spec) (Spec, error) {
	if !keyPattern.MatchString(s.Key) || len(s.Repositories) < 1 || len(s.Repositories) > 32 || !quotapolicy.ValidBytes(s.QuotaBytes) {
		return Spec{}, errors.New("invalid workspace creation request")
	}
	s.Repositories = append([]Repository(nil), s.Repositories...)
	manifest := checkpointset.Manifest{Version: checkpointset.Version, BenchID: strings.Repeat("0", 32), GenerationID: strings.Repeat("1", 32), CheckpointRepository: s.CheckpointRepository}
	for i := range s.Repositories {
		r := &s.Repositories[i]
		if r.Branch == "" {
			r.Branch = "bench/" + s.Key
		}
		if r.Base == "" {
			r.Base = "main"
		}
		if r.Branch == "superbench" || r.Branch == "superbench/checkpoints" || strings.HasPrefix(r.Branch, "superbench/checkpoints/") {
			return Spec{}, errors.New("workspace task branch conflicts with checkpoint metadata namespace")
		}
		for _, branch := range []string{r.Branch, r.Base} {
			if !utf8.ValidString(branch) || len(branch) > 255 || branch == "HEAD" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") || !model.ValidBranchRef("refs/heads/"+branch) {
				return Spec{}, errors.New("invalid workspace short branch")
			}
		}
		pin, err := checkpointset.PinRef(manifest.BenchID, manifest.GenerationID, r.Name)
		if err != nil {
			return Spec{}, err
		}
		manifest.Members = append(manifest.Members, checkpointset.Member{Name: r.Name, Repository: r.Repository, TaskRef: "refs/heads/" + r.Branch, PinRef: pin, ObjectFormat: "sha1", Commit: strings.Repeat("0", 40)})
	}
	if err := manifest.Validate(); err != nil {
		return Spec{}, err
	}
	return s, nil
}

func (p Planner) Select(ctx context.Context, spec Spec) (Plan, error) {
	if spec.TransferID != "" {
		return Plan{}, errors.New("task-ref transfer requires approved journal staging")
	}
	s, err := Normalize(spec)
	if err != nil {
		return Plan{}, err
	}
	if p.Git == nil || !filepath.IsAbs(p.Mirrors.Data) || !filepath.IsAbs(p.Mirrors.State) {
		return Plan{}, errors.New("workspace planning requires absolute storage paths and managed Git")
	}
	timeout := p.Mirrors.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return Plan{}, errors.New("invalid workspace source timeout")
	}
	plan := Plan{Key: s.Key, Root: filepath.Join(p.Mirrors.Data, "benches", s.Key), CheckpointRepository: s.CheckpointRepository, QuotaBytes: s.QuotaBytes}
	for _, r := range s.Repositories {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		member, err := p.selectMember(ctx, timeout, plan.Root, r)
		if err != nil {
			return Plan{}, err
		}
		quota := member.RepositoryQuotaBytes
		// Every non-default declaration describes the same total bench budget.
		// Conflicting member declarations require an explicit source/config fix;
		// summing them would silently multiply the host's allocation policy.
		if quota != 0 {
			if plan.QuotaBytes != 0 && plan.QuotaBytes != quota {
				return Plan{}, errors.New("workspace repository quota declarations conflict with the total budget")
			}
			plan.QuotaBytes = quota
		}
		plan.Members = append(plan.Members, member)
	}
	return plan, nil
}

func (p Planner) selectMember(ctx context.Context, timeout time.Duration, root string, r Repository) (Member, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mirror, err := p.Mirrors.Prepare(ctx, r.Repository)
	if err != nil {
		return Member{}, err
	}
	// Missing task refs select the declared base. A failed command is not
	// evidence of absence. The pattern can also match child refs.
	ref := "refs/remotes/origin/" + r.Branch
	exists, err := p.Git(ctx, mirror, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return Member{}, err
	}
	found := false
	for _, actual := range strings.Split(strings.TrimSpace(exists), "\n") {
		found = found || actual == ref
	}
	if !found {
		ref = "refs/remotes/origin/" + r.Base
	}
	source, err := p.Git(ctx, mirror, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return Member{}, err
	}
	format, err := p.Git(ctx, mirror, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return Member{}, err
	}
	source, format = strings.TrimSpace(source), strings.TrimSpace(format)
	if !model.ValidCommit(source) || format != "sha1" && format != "sha256" || format == "sha1" && len(source) != 40 || format == "sha256" && len(source) != 64 {
		return Member{}, errors.New("invalid workspace source object")
	}
	quota, err := setup.QuotaAtContext(ctx, mirror, source)
	if err != nil {
		return Member{}, err
	}
	return Member{Name: r.Name, Repository: r.Repository, Branch: r.Branch, Base: r.Base, Path: filepath.Join(root, r.Name), Mirror: mirror, Source: source, ObjectFormat: format, RepositoryQuotaBytes: quota}, nil
}

// FromRecord copies the pinned source vector without selecting new sources or granting authority.
func FromRecord(b *model.Bench) Plan {
	p := Plan{Key: b.Key, Root: b.Path, QuotaBytes: b.RequestedQuotaBytes, CheckpointRepository: b.Workspace.CheckpointRepository}
	for _, v := range b.Workspace.Members {
		p.Members = append(p.Members, Member{Name: v.Name, Repository: v.Repository, Branch: v.Branch, Base: v.Base, Path: v.Path, Mirror: v.Mirror, Source: v.Source, ObjectFormat: v.ObjectFormat, RepositoryQuotaBytes: v.RequestedQuotaBytes})
	}
	return p
}
