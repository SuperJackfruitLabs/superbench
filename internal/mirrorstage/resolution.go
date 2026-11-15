package mirrorstage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

const RootRemovalReviewVersion = "superbench.mirror-stage-root-disposition.v1"
const MaxRootRemovalReview = MaxRecordBytes

// RootRemovalReview is fresh single-use authority to remove the CURRENT EMPTY
// directory, not a claim that it is the originally owned staging directory.
type RootRemovalReview struct {
	Version, ID, PlanDigest, RemovalDigest, State, Parent, Stage string
	ParentIdentity, RootIdentity                                 Identity
	ObservedAt                                                   time.Time
	Digest                                                       string
}

func (p RootRemovalReview) digest() string { p.Digest = ""; return digest(p) }
func (p RootRemovalReview) Bytes() ([]byte, error) {
	if p.Version != RootRemovalReviewVersion || !validID(p.ID) || !validHash(p.PlanDigest, 32) || !validHash(p.RemovalDigest, 32) || !utc(p.ObservedAt) || p.Digest != p.digest() || p.Stage != filepath.Join(p.Parent, ".clone-"+p.ID) || p.RootIdentity.Inode == 0 || p.RootIdentity.Device != p.ParentIdentity.Device || os.FileMode(p.RootIdentity.Mode).Type() != os.ModeDir {
		return nil, errors.New("invalid current-empty-stage disposition approval")
	}
	for _, path := range []string{p.State, p.Parent} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, errors.New("canonical disposition paths required")
		}
	}
	raw, e := json.Marshal(p)
	if len(raw) > MaxRootRemovalReview {
		return nil, errors.New("root disposition approval exceeds bound")
	}
	return raw, e
}
func ParseRootRemovalReview(raw []byte) (*RootRemovalReview, error) {
	if len(raw) > MaxRootRemovalReview {
		return nil, errors.New("root disposition approval exceeds bound")
	}
	var p RootRemovalReview
	if e := decode(raw, &p); e != nil {
		return nil, e
	}
	canonical, e := p.Bytes()
	if e != nil {
		return nil, e
	}
	if !compactEqual(raw, canonical) {
		return nil, errors.New("noncanonical root disposition approval")
	}
	return &p, nil
}
func (o *operation) approved(id string) (*Plan, error) {
	if !validID(id) {
		return nil, errors.New("valid mirror stage id required")
	}
	var p Plan
	if e := o.read(id+"-plan.json", &p, MaxPlan); e != nil {
		return nil, e
	}
	if _, e := p.Bytes(); e != nil {
		return nil, e
	}
	if p.Intent.ID != id {
		return nil, errors.New("collection approval id changed")
	}
	if e := o.guard(p); e != nil {
		return nil, e
	}
	return &p, nil
}
func (o *operation) resolutionBinding(id string) (*Plan, removal, error) {
	var r removal
	p, e := o.approved(id)
	if e != nil {
		return nil, r, e
	}
	ok, e := o.removing(*p)
	if e != nil {
		return nil, r, e
	}
	if !ok {
		return nil, r, errors.New("durable original root removal intent required")
	}
	if e = o.read(p.removalName(), &r, MaxRecordBytes); e != nil {
		return nil, r, e
	}
	return p, r, nil
}
func (m Manager) InspectRootRemoval(ctx context.Context, id string) (*RootRemovalReview, error) {
	var result *RootRemovalReview
	e := m.within(ctx, func(o *operation) error {
		p, r, e := o.resolutionBinding(id)
		if e != nil {
			return e
		}
		root, e := o.parent.child(filepath.Base(p.Intent.Stage))
		if e != nil {
			return e
		}
		defer root.close()
		if root.id.Device != o.parent.id.Device {
			return errors.New("returned stage is on another filesystem")
		}
		if e = empty(root); e != nil {
			return e
		}
		v := RootRemovalReview{Version: RootRemovalReviewVersion, ID: id, PlanDigest: p.Digest, RemovalDigest: digest(r), State: o.state.path, Parent: o.parent.path, Stage: p.Intent.Stage, ParentIdentity: o.parent.id, RootIdentity: root.id, ObservedAt: time.Now().UTC()}
		v.Digest = v.digest()
		if _, e = v.Bytes(); e != nil {
			return e
		}
		if e = o.guard(*p); e != nil {
			return e
		}
		if e = root.check(); e != nil {
			return e
		}
		result = &v
		return nil
	})
	return result, e
}
func (m Manager) ReconcileRootRemoval(ctx context.Context, v RootRemovalReview) (*Result, error) {
	if _, e := v.Bytes(); e != nil {
		return nil, e
	}
	var result *Result
	e := m.within(ctx, func(o *operation) error {
		p, r, e := o.resolutionBinding(v.ID)
		if e != nil {
			return e
		}
		if v.PlanDigest != p.Digest || v.RemovalDigest != digest(r) || v.State != o.state.path || v.Parent != o.parent.path || v.ParentIdentity != o.parent.id || v.Stage != p.Intent.Stage {
			return errors.New("root disposition authority changed")
		}
		name := v.ID + "-review-" + v.Digest + ".json"
		var saved RootRemovalReview
		e = o.read(name, &saved, MaxRootRemovalReview)
		if e == nil {
			if !reflect.DeepEqual(saved, v) {
				return errors.New("root disposition approval changed")
			}
			if e = o.absent(*p); e != nil {
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		} else {
			root, e := o.parent.child(filepath.Base(v.Stage))
			if e != nil {
				return e
			}
			defer root.close()
			if root.id != v.RootIdentity {
				return errors.New("current stage root changed after review")
			}
			if e = empty(root); e != nil {
				return e
			}
			if e = o.guard(*p); e != nil {
				return e
			}
			if e = o.capacity(3); e != nil {
				return e
			}
			if e = saveBound(o.collection, name, v); e != nil {
				return e
			}
			if e = o.guard(*p); e != nil {
				return e
			}
			if e = root.check(); e != nil {
				return e
			}
			if e = empty(root); e != nil {
				return e
			}
			if e = o.parent.root.Remove(filepath.Base(v.Stage)); e != nil {
				return e
			}
			if e = o.parent.sync(); e != nil {
				return e
			}
		}
		if e = o.finish(*p); e != nil {
			return e
		}
		result = &Result{ID: v.ID, Digest: p.Digest, Completed: true}
		return nil
	})
	return result, e
}
