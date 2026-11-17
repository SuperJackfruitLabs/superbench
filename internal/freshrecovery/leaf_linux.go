//go:build linux

package freshrecovery

import (
	"context"
	"errors"
	"fmt"
	"path"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshretirement"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"golang.org/x/sys/unix"
)

// step runs one recovery action under Recover with the shared provenance
// sequence: gate, retained inspection, the selected allocation under the
// allocation lock, then the same inspection again and the policy recheck.
// bootstrap, when set, must equal the retained inspection before action runs.
func step(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec, bootstrap *payloadbootstrap.Inspection, action func(payloadbootstrap.Recovery, payloadbootstrap.Inspection, freshretirement.Selection, func() error) error) error {
	if m == nil || action == nil {
		return ErrRecovery
	}
	return m.Recover(ctx, s, func(r payloadbootstrap.Recovery) error {
		if err := r.Gate(); err != nil {
			return err
		}
		i, err := r.Inspection()
		if err != nil {
			return err
		}
		if bootstrap != nil && i != *bootstrap {
			return ErrRecovery
		}
		err = freshretirement.Select(ctx, r.Storage, s.Key, func(sel freshretirement.Selection, recheck, _ func() error) error {
			return action(r, i, sel, recheck)
		})
		if err != nil {
			return err
		}
		if err = r.Gate(); err != nil {
			return err
		}
		after, err := r.Inspection()
		if err != nil {
			return err
		}
		if after != i {
			return ErrRecovery
		}
		return r.Check()
	})
}

// PlanLeaf observes a present Fresh leaf that its phase allows root to remove
// and returns the plan for review. It writes nothing.
func PlanLeaf(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec) (LeafPlan, error) {
	var plan LeafPlan
	err := step(ctx, m, s, nil, func(r payloadbootstrap.Recovery, i payloadbootstrap.Inspection, sel freshretirement.Selection, recheck func() error) error {
		benches, err := openBenches(r.Storage.Root, sel.Approval.Parent)
		if err != nil {
			return err
		}
		defer benches.Close()
		h, err := observe(benches, s.Key, payloadbootstrap.FixedDirectories(s))
		if errors.Is(err, errAbsent) {
			return ErrRecovery
		}
		if err != nil {
			return err
		}
		defer h.close()
		plan = LeafPlan{LeafVersion, i, sel.Approval, h.Leaf}
		if err = plan.validate(s, r.Storage); err != nil {
			return err
		}
		if _, err = encode(plan); err != nil {
			return err
		}
		return recheck()
	})
	if err != nil {
		return LeafPlan{}, err
	}
	return plan, nil
}

// RemoveLeaf retains the exact reviewed plan, then removes the fixed directories
// present (allocating (b) only), in reverse creation order, and the empty leaf.
// Every removal is unlinkat(AT_REMOVEDIR) relative to a held fd, checked by
// node, and fsynced. A retry finding the leaf absent succeeds only with the
// same retained approval. Errors after retention may follow effects.
func RemoveLeaf(ctx context.Context, m *payloadbootstrap.Manager, s payloadbootstrap.Spec, plan LeafPlan) (Result, error) {
	raw, err := encode(plan)
	if err != nil {
		return Result{}, err
	}
	retained := false
	err = step(ctx, m, s, &plan.Bootstrap, func(r payloadbootstrap.Recovery, _ payloadbootstrap.Inspection, sel freshretirement.Selection, recheck func() error) error {
		if err := plan.validate(s, r.Storage); err != nil || sel.Approval != plan.Storage {
			return ErrRecovery
		}
		benches, err := openBenches(r.Storage.Root, plan.Storage.Parent)
		if err != nil {
			return err
		}
		defer benches.Close()
		h, err := observe(benches, s.Key, payloadbootstrap.FixedDirectories(s))
		if errors.Is(err, errAbsent) {
			if err = retain(r.Storage.Ledger, s.Key, "leaf", raw, true); err != nil {
				return err
			}
			retained = true
			if err = unix.Fsync(int(benches.Fd())); err != nil {
				return err
			}
			return recheck()
		}
		if err != nil {
			return err
		}
		defer h.close()
		// A crashed earlier attempt may have removed a suffix of the fixed names.
		current := plan
		current.Leaf = h.Leaf
		if h.Node != plan.Leaf.Node || h.ProjectID != plan.Leaf.ProjectID || !prefixOf(h.Fixed, plan.Leaf.Fixed) || current.validate(s, r.Storage) != nil {
			return ErrRecovery
		}
		if err = recheck(); err != nil {
			return err
		}
		if err = retain(r.Storage.Ledger, s.Key, "leaf", raw, false); err != nil {
			return err
		}
		retained = true
		if err = r.Gate(); err != nil {
			return err
		}
		if len(h.Fixed) != 0 {
			for k := len(h.Fixed) - 1; k >= 0; k-- {
				e := h.Fixed[k]
				if err = rmdirAt(h.dirs[path.Dir(e.Name)], h.dirs[e.Name], path.Base(e.Name), e.Node); err != nil {
					return err
				}
			}
			if err = r.Gate(); err != nil {
				return err
			}
		}
		if err = rmdirAt(benches, h.dirs["."], s.Key, h.Node); err != nil {
			return err
		}
		if err = r.Gate(); err != nil {
			return err
		}
		return recheck()
	})
	if err != nil {
		if retained {
			err = fmt.Errorf("effects may have occurred; retry the same plan: %w", err)
		}
		return Result{}, err
	}
	return Result{ResultVersion, s.Key, "remove-empty-bootstrap-leaf", digest(raw)}, nil
}
