package maintenance

import (
	"context"
	"errors"
	"syscall"
	"time"
)

func (m Manager) Tick(ctx context.Context) (Report, error) {
	report := Report{At: time.Now().UTC(), Status: "disabled"}
	unlock, e := m.gate(true)
	if errors.Is(e, syscall.EWOULDBLOCK) || errors.Is(e, syscall.EAGAIN) {
		report.Status = "busy"
		return report, nil
	}
	if e != nil {
		return report, e
	}
	defer unlock()
	var c Config
	if e = m.read("retention-policy.json", &c); missing(e) {
		return report, nil
	} else if e != nil {
		return report, e
	}
	if e = c.Proposal.Validate(m.Root); e != nil {
		return report, e
	}
	if !c.Enabled {
		return report, nil
	}
	report.PolicyDigest = c.Proposal.Digest
	if e = ctx.Err(); e != nil {
		return report, e
	}
	var pending Pending
	e = m.read("retention-auto-pending.json", &pending)
	if missing(e) {
		p, e := m.Store.PlanCollection(c.Proposal.Policy.LogAge.Value(), c.Proposal.Policy.MetadataAge.Value(), c.Proposal.Policy.Limit)
		if e != nil {
			return m.failure(report, e)
		}
		bounded, deferred, e := bound(*p, c.Proposal.Policy)
		if e != nil {
			return m.failure(report, e)
		}
		report.Deferred = deferred
		if len(bounded.Logs)+len(bounded.Records)+len(bounded.Collections) == 0 {
			report.Status = "no candidates"
			return report, m.write("retention-last.json", report)
		}
		if e = ctx.Err(); e != nil {
			return report, e
		}
		pending = Pending{PolicyDigest: c.Proposal.Digest, Plan: bounded}
		if e = m.write("retention-auto-pending.json", pending); e != nil {
			return m.failure(report, e)
		}
	} else if e != nil {
		return m.failure(report, e)
	}
	if pending.PolicyDigest != c.Proposal.Digest {
		return m.failure(report, errors.New("pending collection belongs to another policy; reconcile explicitly"))
	}
	if e = validateBatch(pending.Plan, c.Proposal.Policy); e != nil {
		return m.failure(report, e)
	}
	if e = ctx.Err(); e != nil {
		return report, e
	}
	result, e := m.Store.Collect(pending.Plan)
	if e != nil {
		return m.failure(report, e)
	}
	if result.Status != "completed" {
		return m.failure(report, errors.New("collection did not complete"))
	}
	report.Status = "completed"
	report.Collection = &result
	if e = m.write("retention-last.json", report); e != nil {
		return report, e
	}
	return report, m.clearPending()
}
func (m Manager) failure(r Report, e error) (Report, error) {
	r.Status = "blocked"
	r.Error = e.Error()
	if saveErr := m.write("retention-last.json", r); saveErr != nil {
		return r, errors.Join(e, saveErr)
	}
	return r, e
}
func (m Manager) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		if onError != nil {
			onError(errors.New("invalid retention interval"))
		}
		return
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		if _, e := m.Tick(ctx); e != nil && ctx.Err() == nil && onError != nil {
			onError(e)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}

// A canceled prepared batch must be acknowledged, not silently reissued under a
// different policy. This only clears the manager's pointer after core evidence
// says the batch is completed/canceled or absent. Absence is safe because the
// core always journals its own approval before unlinking; this pointer alone
// cannot authorize a new core collection. It never deletes output files itself.
func (m Manager) AcknowledgePending() (Status, error) {
	unlock, e := m.gate(false)
	if e != nil {
		return Status{}, e
	}
	defer unlock()
	var p Pending
	if e = m.read("retention-auto-pending.json", &p); e != nil {
		return Status{}, e
	}
	if e = p.Plan.Validate(m.Root); e != nil {
		return Status{}, e
	}
	items, e := m.Store.Collections()
	if e != nil {
		return Status{}, e
	}
	for _, item := range items {
		if item.Digest == p.Plan.Digest && (item.Status == "completed" || item.Status == "canceled") {
			if e = m.clearPending(); e != nil {
				return Status{}, e
			}
			return m.Status()
		}
	}
	for _, item := range items {
		if item.Digest == p.Plan.Digest {
			return Status{}, errors.New("pending batch still has prepared core evidence; resume or cancel it first")
		}
	}
	if e = m.clearPending(); e != nil {
		return Status{}, e
	}
	return m.Status()
}
