package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/transferguard"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// Lifecycle retains the enclosing host lock across durable transitions and
// remote publication. Stopped must independently prove the held writer fenced;
// Released records release of every retained stage in the final transaction.
type Lifecycle struct {
	// Only the reviewed local repair flow may continue this initialization epoch.
	InitializationRepairID string
	TransferID             string
	State, Data            string
	Timeout                time.Duration
	Within                 func(func(Update) error) error
	Git                    func(context.Context, string, ...string) (string, error)
	Admit                  func() error
	Allocate               func(*model.Bench) error
	Stopped                func(*model.Bench) error
	Prepared               func(*model.Bench) error
	Released               func(*sql.Tx, *model.Bench) error
	NoteCheckpoint         func(context.Context, *model.Bench)
}

func (l Lifecycle) bounded(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := statepath.Validate(l.State, l.Data); err != nil {
		return nil, nil, err
	}
	if filepath.Clean(l.State) != l.State || filepath.Clean(l.Data) != l.Data || l.Git == nil {
		return nil, nil, errors.New("workspace lifecycle requires normalized storage and managed Git")
	}
	timeout := l.Timeout
	if timeout == 0 {
		timeout = repository.DefaultTimeout
	}
	if timeout < 0 || timeout > time.Hour {
		return nil, nil, errors.New("invalid workspace lifecycle timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	if err := ctx.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return ctx, cancel, nil
}

func (l Lifecycle) git(ctx context.Context, dir string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := l.Git(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

func samePhysical(a, b string) bool {
	x, e := filepath.EvalSymlinks(a)
	y, f := filepath.EvalSymlinks(b)
	return e == nil && f == nil && x == y
}

// Inspect validates the registered complete checkout vector without refreshing
// sources, moving refs or changing files. A preparation caller may inspect a
// dirty checkout; checkpoints require all members clean before the first push.
func (l Lifecycle) Inspect(ctx context.Context, b *model.Bench, clean bool) ([]model.WorkspaceCommit, error) {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err := l.inspectRoot(b, clean); err != nil {
		return nil, err
	}
	var vector []model.WorkspaceCommit
	for _, member := range b.Workspace.Members {
		commit, err := l.inspectMember(ctx, member, clean)
		if err != nil {
			return nil, err
		}
		vector = append(vector, commit)
	}
	return vector, nil
}

// InspectMember revalidates one registered target immediately before its cache
// or command effects. Earlier members can change later paths while preparing;
// a whole-vector inspection before those hooks is not sufficient authority.
func (l Lifecycle) InspectMember(ctx context.Context, b *model.Bench, name string, clean bool) (model.WorkspaceCommit, error) {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	defer cancel()
	if err := l.inspectRoot(b, false); err != nil {
		return model.WorkspaceCommit{}, err
	}
	for _, member := range b.Workspace.Members {
		if member.Name == name {
			return l.inspectMember(ctx, member, clean)
		}
	}
	return model.WorkspaceCommit{}, errors.New("workspace member selector is not registered")
}

func (l Lifecycle) inspectRoot(b *model.Bench, clean bool) error {
	if err := workspacepolicy.Validate(b); err != nil {
		return err
	}
	if b.Path != filepath.Join(l.Data, "benches", b.Key) {
		return errors.New("workspace root differs from configured data")
	}
	for _, path := range []string{l.Data, filepath.Join(l.Data, "benches"), b.Path, filepath.Join(l.Data, "mirrors")} {
		if err := physicalDirectory(path, false); err != nil {
			return err
		}
	}
	if clean {
		names := map[string]bool{}
		for _, member := range b.Workspace.Members {
			names[member.Name] = true
		}
		entries, err := os.ReadDir(b.Path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !names[entry.Name()] || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return model.ErrDirty
			}
		}
	}
	return nil
}

func (l Lifecycle) inspectMember(ctx context.Context, member model.WorkspaceMember, clean bool) (model.WorkspaceCommit, error) {
	for _, path := range []string{member.Path, member.Mirror} {
		if err := physicalDirectory(path, false); err != nil {
			return model.WorkspaceCommit{}, err
		}
	}
	top, err := l.git(ctx, member.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	common, err := l.git(ctx, member.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	branch, err := l.git(ctx, member.Path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	if !samePhysical(top, member.Path) || !samePhysical(common, member.Mirror) || branch != "refs/heads/"+member.Branch {
		return model.WorkspaceCommit{}, errors.New("workspace checkout identity changed; preserve and reconcile")
	}
	bare, err := l.git(ctx, member.Mirror, "rev-parse", "--is-bare-repository")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	origin, err := l.git(ctx, member.Mirror, "config", "--null", "--get-all", "remote.origin.url")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	checkoutOrigin, err := l.git(ctx, member.Path, "config", "--null", "--get-all", "remote.origin.url")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	format, err := l.git(ctx, member.Path, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	if bare != "true" || origin != member.Repository+"\x00" || checkoutOrigin != origin || format != member.ObjectFormat {
		return model.WorkspaceCommit{}, errors.New("workspace mirror identity or object format changed")
	}
	if clean {
		status, err := l.git(ctx, member.Path, "status", "--porcelain", "--untracked-files=all")
		if err != nil {
			return model.WorkspaceCommit{}, err
		}
		if status != "" {
			return model.WorkspaceCommit{}, model.ErrDirty
		}
	}
	head, err := l.git(ctx, member.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return model.WorkspaceCommit{}, err
	}
	if !model.ValidCommit(head) || head != strings.ToLower(head) || member.ObjectFormat == "sha1" && len(head) != 40 || member.ObjectFormat == "sha256" && len(head) != 64 {
		return model.WorkspaceCommit{}, errors.New("workspace HEAD has an invalid object identity")
	}
	return model.WorkspaceCommit{Name: member.Name, Commit: head}, nil
}

func (l Lifecycle) owner(b *model.Bench, token string) error {
	if err := workspacepolicy.Validate(b); err != nil {
		return err
	}
	if b.Workspace.Transfer != nil {
		return ErrTransfer
	}
	if b.Workspace.Phase != "ready" && b.Workspace.InitializationRepair != nil && l.InitializationRepairID != b.Workspace.InitializationRepair.ID {
		return ErrInitializationRepair
	}
	if err := transferguard.Authority(b, l.TransferID, l.InitializationRepairID); err != nil {
		return err
	}
	if b.Lease == nil || token == "" || b.Lease.Token != token || b.Workspace.Phase != "prepared" && b.Workspace.Phase != "ready" {
		return errors.New("workspace checkpoint requires its prepared held owner")
	}
	if l.Stopped == nil {
		return errors.New("workspace lifecycle requires stopped-writer authority")
	}
	return l.Stopped(privateRecord(b))
}

func (l Lifecycle) preparation(b *model.Bench) error {
	if l.Prepared == nil {
		return errors.New("workspace initialization requires current preparation authority")
	}
	return l.Prepared(privateRecord(b))
}

func (l Lifecycle) coordinator(b *model.Bench) (checkpointset.Coordinator, error) {
	state, err := filepath.EvalSymlinks(l.State)
	if err != nil {
		return checkpointset.Coordinator{}, err
	}
	c := checkpointset.Coordinator{State: filepath.Join(state, "workspace-checkpoints"), Mirrors: map[string]string{}}
	for _, member := range b.Workspace.Members {
		c.Mirrors[member.Repository] = member.Mirror
	}
	return c, nil
}

// confirm checks both the immutable seal/pins and the current task refs. Recover
// deliberately permits historical seals, so its proof alone cannot release a
// writer whose task branches have since moved elsewhere.
func (l Lifecycle) confirm(ctx context.Context, b *model.Bench, cp *model.WorkspaceCheckpoint) error {
	c, err := l.coordinator(b)
	if err != nil {
		return err
	}
	m, err := workspacepolicy.Manifest(b, cp)
	if err != nil {
		return err
	}
	var repo string
	for _, v := range b.Workspace.Members {
		if v.Name == b.Workspace.CheckpointRepository {
			repo = v.Repository
		}
	}
	got, err := c.Recover(ctx, repo, b.Workspace.ID, cp.Seal, cp.Digest)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, m) {
		return errors.New("workspace checkpoint seal differs from the registered generation")
	}
	for i, member := range b.Workspace.Members {
		ref := "refs/heads/" + member.Branch
		out, err := l.git(ctx, member.Mirror, "ls-remote", "--refs", "origin", ref)
		if err != nil {
			return err
		}
		fields := strings.Fields(out)
		if len(fields) != 2 || fields[0] != cp.Members[i].Commit || fields[1] != ref {
			return errors.New("workspace task branch differs from its complete checkpoint")
		}
	}
	return nil
}

// Checkpoint journals the entire clean HEAD vector before any remote mutation.
// Every failure preserves the committed generation and held owner for retry.
func (l Lifecycle) Checkpoint(ctx context.Context, key, token string) (*model.Bench, error) {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if !keyPattern.MatchString(key) || token == "" || l.Within == nil || l.Stopped == nil {
		return nil, errors.New("workspace checkpoint requires storage and a held owner")
	}
	var committed *model.Bench
	err = l.Within(func(update Update) error {
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := l.ownerContext(ctx, tx, rows[key], token); err != nil {
				return err
			}
			if err := transferguard.Check(ctx, transferguard.Config{State: l.State, Data: l.Data, Git: l.Git, Timeout: l.Timeout}, tx, rows[key]); err != nil {
				return err
			}
			if rows[key].Workspace.Phase == "prepared" {
				if err := l.preparation(rows[key]); err != nil {
					return err
				}
			}
			committed = privateRecord(rows[key])
			return nil
		}); err != nil {
			return err
		}
		vector, err := l.Inspect(ctx, committed, true)
		if err != nil {
			return err
		}
		if committed.Workspace.Pending == nil && committed.Workspace.Checkpoint != nil && reflect.DeepEqual(vector, committed.Workspace.Checkpoint.Members) {
			return l.confirm(ctx, committed, committed.Workspace.Checkpoint)
		}
		if committed.Workspace.Pending == nil {
			generation, err := model.Nonce()
			if err != nil {
				return err
			}
			candidate := privateRecord(committed)
			pending := &model.WorkspaceCheckpoint{GenerationID: generation, Members: append([]model.WorkspaceCommit(nil), vector...)}
			if candidate.Workspace.Checkpoint != nil {
				pending.Previous = candidate.Workspace.Checkpoint.Seal
			}
			manifest, err := workspacepolicy.Manifest(candidate, pending)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(manifest)
			if err != nil {
				return err
			}
			pending.Digest = checkpointset.Digest(raw)
			candidate.Workspace.Pending = pending
			if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !reflect.DeepEqual(rows[key], committed) {
					return errors.New("workspace checkpoint ownership changed")
				}
				rows[key] = privateRecord(candidate)
				return nil
			}); err != nil {
				return err
			}
			committed = privateRecord(candidate)
		} else if !reflect.DeepEqual(vector, committed.Workspace.Pending.Members) {
			return errors.New("workspace HEAD vector differs from its pending checkpoint; preserve and reconcile")
		}
		manifest, err := workspacepolicy.Manifest(committed, committed.Workspace.Pending)
		if err != nil {
			return err
		}
		c, err := l.coordinator(committed)
		if err != nil {
			return err
		}
		c.Gate = func(gateCtx context.Context, actual checkpointset.Manifest) error {
			return update(func(rows benchstate.Database, tx *sql.Tx) error {
				if err := gateCtx.Err(); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if !reflect.DeepEqual(rows[key], committed) || !reflect.DeepEqual(actual, manifest) {
					return errors.New("workspace pending generation or owner changed")
				}
				if err := l.ownerContext(ctx, tx, rows[key], token); err != nil {
					return err
				}
				if err := transferguard.Check(ctx, transferguard.Config{State: l.State, Data: l.Data, Git: l.Git, Timeout: l.Timeout}, tx, rows[key]); err != nil {
					return err
				}
				return nil
			})
		}
		seal, err := c.Complete(ctx, manifest)
		if err != nil {
			return err
		}
		finished := privateRecord(committed)
		finished.Workspace.Checkpoint = finished.Workspace.Pending
		finished.Workspace.Checkpoint.Seal = seal.Commit
		finished.Workspace.Pending = nil
		if seal.BenchID != finished.Workspace.ID || seal.Digest != finished.Workspace.Checkpoint.Digest {
			return errors.New("workspace completed checkpoint differs from its pending generation")
		}
		if finished.Workspace.Phase == "ready" && l.NoteCheckpoint != nil {
			l.NoteCheckpoint(ctx, finished)
		}
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !reflect.DeepEqual(rows[key], committed) {
				return errors.New("workspace ownership changed before checkpoint save")
			}
			if err := l.ownerContext(ctx, tx, rows[key], token); err != nil {
				return err
			}
			if err := transferguard.Check(ctx, transferguard.Config{State: l.State, Data: l.Data, Git: l.Git, Timeout: l.Timeout}, tx, rows[key]); err != nil {
				return err
			}
			if rows[key].Workspace.Phase == "prepared" {
				if err := l.preparation(rows[key]); err != nil {
					return err
				}
			}
			heads, err := l.Inspect(ctx, rows[key], true)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(heads, committed.Workspace.Pending.Members) {
				return errors.New("workspace HEAD vector changed before checkpoint save")
			}
			rows[key] = privateRecord(finished)
			return nil
		}); err != nil {
			return err
		}
		committed = privateRecord(finished)
		return nil
	})
	return privateRecord(committed), err
}

// FinishInitialization requires successful preparation, a complete checkpoint,
// and an independent remote/stopped-writer proof before publishing readiness.
func (l Lifecycle) FinishInitialization(ctx context.Context, key, token string) (*model.Bench, error) {
	ctx, cancel, err := l.bounded(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	if !keyPattern.MatchString(key) || token == "" || l.Within == nil || l.Stopped == nil || l.Prepared == nil || l.Released == nil {
		return nil, errors.New("workspace readiness requires storage, fencing and retained-history release")
	}
	var committed *model.Bench
	// Reject ready operational leases before Checkpoint can publish anything.
	if err := l.Within(func(update Update) error {
		return update(func(rows benchstate.Database, tx *sql.Tx) error {
			b := rows[key]
			if err := l.initializationContext(ctx, tx, b, token); err != nil {
				return err
			}
			if err := transferguard.Check(ctx, transferguard.Config{State: l.State, Data: l.Data, Git: l.Git, Timeout: l.Timeout}, tx, b); err != nil {
				return err
			}
			committed = privateRecord(b)
			return ctx.Err()
		})
	}); err != nil {
		return privateRecord(committed), err
	}
	checkpointed, err := l.Checkpoint(ctx, key, token)
	if checkpointed != nil {
		committed = checkpointed
	}
	if err != nil {
		return committed, err
	}
	err = l.Within(func(update Update) error {
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if !reflect.DeepEqual(b, committed) {
				return errors.New("workspace initialization ownership changed")
			}
			if err := l.initializationContext(ctx, tx, b, token); err != nil {
				return err
			}
			if err := transferguard.Check(ctx, transferguard.Config{State: l.State, Data: l.Data, Git: l.Git, Timeout: l.Timeout}, tx, b); err != nil {
				return err
			}
			if b.Workspace.Pending != nil || b.Workspace.Checkpoint == nil {
				return errors.New("workspace initialization has no complete checkpoint")
			}
			return nil
		}); err != nil {
			return err
		}
		if err := l.confirm(ctx, committed, committed.Workspace.Checkpoint); err != nil {
			return err
		}
		var finished *model.Bench
		if err := update(func(rows benchstate.Database, tx *sql.Tx) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			b := rows[key]
			if !reflect.DeepEqual(b, committed) {
				return errors.New("workspace initialization ownership changed")
			}
			if err := l.initializationContext(ctx, tx, b, token); err != nil {
				return err
			}
			if err := transferguard.Check(ctx, transferguard.Config{State: l.State, Data: l.Data, Git: l.Git, Timeout: l.Timeout}, tx, b); err != nil {
				return err
			}
			vector, err := l.Inspect(ctx, b, true)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(vector, b.Workspace.Checkpoint.Members) {
				return errors.New("workspace initialization differs from its complete checkpoint")
			}
			if err := l.preparation(b); err != nil {
				return err
			}
			if err := l.Released(tx, privateRecord(b)); err != nil {
				return err
			}
			finished = privateRecord(b)
			finished.Lease = nil
			finished.Workspace.Phase = "ready"
			rows[key] = finished
			return ctx.Err()
		}); err != nil {
			return err
		}
		committed = privateRecord(finished)
		return nil
	})
	return privateRecord(committed), err
}

func (l Lifecycle) initialization(b *model.Bench, token string) error {
	if err := l.owner(b, token); err != nil {
		return err
	}
	if b.Workspace.Phase != "prepared" || b.Lease.Holder != "initialization" || b.Lease.Continuation == "" || b.Lease.StopRequested {
		return errors.New("workspace initialization is not prepared under its original continuation")
	}
	return l.preparation(b)
}
