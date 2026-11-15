package mirrorstage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/statepath"
)

type Manager struct {
	State, Parent string
	Within        func(func() error) error
	// VerifyControl must reconcile/fence the original managed Git control scopes.
	// Configured provenance, a lock or age alone does not satisfy this callback.
	VerifyControl func(context.Context, string, string) error
}
type operation struct {
	m                                  Manager
	ctx                                context.Context
	state, parent, journal, collection *boundDir
}

func (m Manager) within(ctx context.Context, fn func(*operation) error) error {
	if m.Within == nil || m.VerifyControl == nil {
		return errors.New("collection requires host lock and managed writer reconciliation")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := statepath.Validate(m.State, m.Parent); e != nil {
		return e
	}
	return m.Within(func() error {
		state, e := physical(m.State)
		if e != nil {
			return e
		}
		parent, e := physical(m.Parent)
		if e != nil {
			return e
		}
		s, e := bind(state)
		if e != nil {
			return e
		}
		defer s.close()
		p, e := bind(parent)
		if e != nil {
			return e
		}
		defer p.close()
		j, e := openJournal(s, false)
		if e != nil {
			return e
		}
		defer j.close()
		o := &operation{m: m, ctx: ctx, state: s, parent: p, journal: j}
		defer func() {
			if o.collection != nil {
				o.collection.close()
			}
		}()
		if _, e = s.root.Lstat("mirror-stage-collections"); e == nil {
			o.collection, e = s.child("mirror-stage-collections")
			if e != nil {
				return e
			}
			if os.FileMode(o.collection.id.Mode).Perm() != 0700 {
				return errors.New("private collection journal required")
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		return fn(o)
	})
}
func (o *operation) guard(p Plan) error {
	if e := o.ctx.Err(); e != nil {
		return e
	}
	if p.Intent.State != o.state.path || p.Intent.Parent != o.parent.path || p.Intent.ParentIdentity != o.parent.id || p.Intent.ControlRoot == "" {
		return errors.New("collection authority differs from configured managed stage")
	}
	if e := o.m.VerifyControl(o.ctx, p.Intent.State, p.Intent.ControlRoot); e != nil {
		return e
	}
	if e := o.ctx.Err(); e != nil {
		return e
	}
	dirs := []*boundDir{o.state, o.parent, o.journal}
	if o.collection != nil {
		dirs = append(dirs, o.collection)
	}
	if e := checkDirs(dirs...); e != nil {
		return e
	}
	if p.Published != nil {
		mirror, e := o.parent.child(filepath.Base(p.Intent.Mirror))
		if e != nil {
			return e
		}
		defer mirror.close()
		if mirror.id != *p.Published {
			return errors.New("published mirror identity changed")
		}
	}
	return nil
}
func (o *operation) source(p Plan) error {
	intent, root, e := Load(o.state.path, o.parent.path, p.Intent.ID)
	if e != nil {
		return e
	}
	if *intent != p.Intent || *root != p.Root {
		return errors.New("stage authority changed since approval")
	}
	return nil
}
func (o *operation) openCollection(p Plan) error {
	if o.collection != nil {
		return nil
	}
	if e := o.guard(p); e != nil {
		return e
	}
	if e := o.state.root.Mkdir("mirror-stage-collections", 0700); e != nil {
		return e
	}
	if e := o.state.sync(); e != nil {
		return e
	}
	d, e := o.state.child("mirror-stage-collections")
	if e != nil {
		return e
	}
	o.collection = d
	return nil
}
func (o *operation) capacity(n int) error {
	f, e := o.collection.root.Open(".")
	if e != nil {
		return e
	}
	entries, e := f.ReadDir(MaxJournalEntries + 1)
	closeErr := f.Close()
	if e != nil && e != io.EOF {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries)+n > MaxJournalEntries {
		return errors.New("collection journal budget exhausted; review completed metadata retirement")
	}
	return nil
}
func (o *operation) read(name string, v any, limit int) error {
	if o.collection == nil {
		return os.ErrNotExist
	}
	return readLimit(o.collection, name, v, limit)
}
func (m Manager) Inspect(ctx context.Context, id string) (*Plan, error) {
	var result *Plan
	e := m.within(ctx, func(o *operation) error {
		if !validID(id) {
			return errors.New("valid mirror stage id required")
		}
		var retained Plan
		e := o.read(id+"-plan.json", &retained, MaxPlan)
		if e == nil {
			if _, e = retained.Bytes(); e != nil {
				return e
			}
			if retained.Intent.ID != id {
				return errors.New("collection approval id differs from journal")
			}
			if e = o.guard(retained); e != nil {
				return e
			}
			result = &retained
			return nil // Historical immutable approval, not a fresh tree.
		} else if !os.IsNotExist(e) {
			return e
		}
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
		if e = o.guard(p); e != nil {
			return e
		}
		if e = o.source(p); e != nil {
			return e
		}
		if e = CheckRoot(*intent, *root); e != nil {
			return e
		}
		p.Digest = p.digest()
		if _, e = p.Bytes(); e != nil {
			return e
		}
		result = &p
		return nil
	})
	return result, e
}
func samePlan(a, b Plan) bool { return reflect.DeepEqual(a, b) }
