package mirrorstage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
)

type removal struct {
	Version, ID, PlanDigest string
	StartedAt               time.Time
}
type completed struct {
	Version, ID, PlanDigest string
	CompletedAt             time.Time
}

func (p Plan) planName() string    { return p.Intent.ID + "-plan.json" }
func (p Plan) removalName() string { return p.Intent.ID + "-removal.json" }
func (p Plan) doneName() string    { return p.Intent.ID + "-done.json" }
func (o *operation) absent(p Plan) error {
	if e := checkDirs(o.parent); e != nil {
		return e
	}
	_, e := o.parent.root.Lstat(filepath.Base(p.Intent.Stage))
	if !os.IsNotExist(e) {
		return errors.New("stage root is present or absence is unproven; preserve it")
	}
	return o.parent.check()
}
func (o *operation) done(p Plan) (bool, error) {
	var d completed
	e := o.read(p.doneName(), &d, MaxRecordBytes)
	if os.IsNotExist(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if d.Version != PlanVersion || d.ID != p.Intent.ID || d.PlanDigest != p.Digest || !utc(d.CompletedAt) {
		return false, errors.New("collection completion differs from approval")
	}
	return true, nil
}
func (o *operation) removing(p Plan) (bool, error) {
	var r removal
	e := o.read(p.removalName(), &r, MaxRecordBytes)
	if os.IsNotExist(e) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if r.Version != PlanVersion || r.ID != p.Intent.ID || r.PlanDigest != p.Digest || !utc(r.StartedAt) {
		return false, errors.New("root removal intent differs from approval")
	}
	return true, nil
}
func (m Manager) Apply(ctx context.Context, p Plan) (*Result, error) {
	if _, e := p.Bytes(); e != nil {
		return nil, e
	}
	var result *Result
	e := m.within(ctx, func(o *operation) error {
		if e := o.apply(p); e != nil {
			return e
		}
		result = &Result{ID: p.Intent.ID, Digest: p.Digest, Completed: true}
		return nil
	})
	return result, e
}
func (o *operation) apply(p Plan) error {
	var stored Plan
	e := o.read(p.planName(), &stored, MaxPlan)
	if os.IsNotExist(e) {
		// No approval means no missing entries are accepted as progress.
		if e = o.source(p); e != nil {
			return e
		}
		// Only persisted provenance may reach the privileged fencing callback.
		if e = o.guard(p); e != nil {
			return e
		}
		if e = CheckRoot(p.Intent, p.Root); e != nil {
			return e
		}
		tree, e := reap.Snapshot(p.Intent.Stage, nil)
		if e != nil {
			return e
		}
		if digest(tree) != digest(p.Tree) {
			return errors.New("stage changed since review; no deletion authorized")
		}
		if e = o.guard(p); e != nil {
			return e
		}
		if e = o.source(p); e != nil {
			return e
		}
		if e = o.openCollection(p); e != nil {
			return e
		}
		// Leave one slot for the durable forget marker after completion.
		if e = o.capacity(4); e != nil {
			return e
		}
		if e = saveLimit(o.collection, p.planName(), p, MaxPlan); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else if !samePlan(stored, p) {
		return errors.New("different collection approval already retained")
	}
	if e = o.guard(p); e != nil {
		return e
	}
	done, e := o.done(p)
	if e != nil {
		return e
	}
	if done {
		if e = o.absent(p); e != nil {
			return e
		}
		return o.retireSource(p)
	}
	removing, e := o.removing(p)
	if e != nil {
		return e
	}
	if removing {
		// There is deliberately no inode/mode based exception for a present root.
		if e = o.absent(p); e != nil {
			return e
		}
		return o.finish(p)
	}
	if e = o.source(p); e != nil {
		return e
	}
	if e = CheckRoot(p.Intent, p.Root); e != nil {
		return e
	}
	remaining, e := reap.Remaining(p.Intent.Stage, p.Tree)
	if e != nil {
		return e
	}
	stage, e := o.parent.child(filepath.Base(p.Intent.Stage))
	if e != nil {
		return e
	}
	defer stage.close()
	if stage.id != p.Root.Identity {
		return errors.New("stage root differs from approval")
	}
	parents := map[string]reap.Entry{}
	for _, entry := range p.Tree.Entries {
		if os.FileMode(entry.Mode).IsDir() {
			parents[entry.Path] = entry
		}
	}
	for index := len(remaining.Entries) - 1; index >= 0; index-- {
		if e = o.guard(p); e != nil {
			return e
		}
		if e = o.source(p); e != nil {
			return e
		}
		if e = stage.check(); e != nil {
			return e
		}
		if e = deleteEntry(stage, remaining.Entries[index], parents); e != nil {
			return e
		}
	}
	if e = o.guard(p); e != nil {
		return e
	}
	if e = o.source(p); e != nil {
		return e
	}
	if e = stage.check(); e != nil {
		return e
	}
	if e = empty(stage); e != nil {
		return e
	}
	if e = o.capacity(3); e != nil {
		return e
	}
	if e = saveBound(o.collection, p.removalName(), removal{Version: PlanVersion, ID: p.Intent.ID, PlanDigest: p.Digest, StartedAt: time.Now().UTC()}); e != nil {
		return e
	}
	if e = o.guard(p); e != nil {
		return e
	}
	if e = o.source(p); e != nil {
		return e
	}
	if e = stage.check(); e != nil {
		return e
	}
	if e = empty(stage); e != nil {
		return e
	}
	if e = o.parent.root.Remove(filepath.Base(p.Intent.Stage)); e != nil {
		return e
	}
	if e = o.parent.sync(); e != nil {
		return e
	}
	return o.finish(p)
}
func (o *operation) finish(p Plan) error {
	if e := o.guard(p); e != nil {
		return e
	}
	if e := o.absent(p); e != nil {
		return e
	}
	done, e := o.done(p)
	if e != nil {
		return e
	}
	if !done {
		if e = o.source(p); e != nil {
			return e
		}
		if e = o.capacity(2); e != nil {
			return e
		}
		if e = saveBound(o.collection, p.doneName(), completed{Version: PlanVersion, ID: p.Intent.ID, PlanDigest: p.Digest, CompletedAt: time.Now().UTC()}); e != nil {
			return e
		}
	}
	return o.retireSource(p)
}
func (o *operation) retireSource(p Plan) error {
	// Completion is durable before removing the two exact ownership records.
	for _, record := range []struct {
		name  string
		value any
	}{{p.Intent.ID + "-root.json", p.Root}, {p.Intent.ID + ".json", p.Intent}} {
		if e := o.guard(p); e != nil {
			return e
		}
		if e := o.absent(p); e != nil {
			return e
		}
		var actual any
		if _, ok := record.value.(Root); ok {
			actual = &Root{}
		} else {
			actual = &Intent{}
		}
		e := readBound(o.journal, record.name, actual)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if digest(actual) != digest(record.value) {
			return errors.New("ownership record changed; preserve it")
		}
		if e = o.journal.root.Remove(record.name); e != nil {
			return e
		}
		if e = o.journal.sync(); e != nil {
			return e
		}
	}
	return nil
}
func empty(dir *boundDir) error {
	if e := dir.check(); e != nil {
		return e
	}
	f, e := dir.root.Open(".")
	if e != nil {
		return e
	}
	entries, e := f.ReadDir(1)
	closeErr := f.Close()
	if e != nil && e != io.EOF {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) != 0 {
		return errors.New("stage directory is not empty")
	}
	return dir.check()
}

// CleanupPublished removes only an empty, owned managed stage after publication.
// Cooperative successful clones are deliberately retained for separate review.
func (m Manager) CleanupPublished(ctx context.Context, id string) error {
	return m.within(ctx, func(o *operation) error {
		intent, root, e := Load(o.state.path, o.parent.path, id)
		if e != nil {
			return e
		}
		p := Plan{Version: PlanVersion, Intent: *intent, Root: *root, ObservedAt: time.Now().UTC()}
		if e = o.guard(p); e != nil {
			return e
		}
		if e = CheckRoot(*intent, *root); e != nil {
			return e
		}
		p.Tree, e = reap.Snapshot(intent.Stage, nil)
		if e != nil {
			return e
		}
		if len(p.Tree.Entries) != 0 {
			return errors.New("published stage retains files; explicit review required")
		}
		mirror, e := o.parent.child(filepath.Base(intent.Mirror))
		if e != nil {
			return e
		}
		defer mirror.close()
		observed := mirror.id
		p.Published = &observed
		p.Digest = p.digest()
		if _, e = p.Bytes(); e != nil {
			return e
		}
		if e = o.apply(p); e != nil {
			return e
		}
		return o.forget(p.Intent.ID, p.Digest)
	})
}
