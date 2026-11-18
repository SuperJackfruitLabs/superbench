// Package workspaceidle is the daemon's idle grace: a maintenance sweep that
// reclaims a released Fresh workspace once it has been idle longer than the
// activated grace. A bench is a cache over its remote task branches, so the
// sweep only starts the operator's own reclaim, which decides from the
// daemon's records, the payload helper and the remote again before any effect.
package workspaceidle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/benchstate"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshtree"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacepolicy"
)

// ErrNotIdle refuses a reclaim whose record no longer passes the sweep's
// selection when the reclaim decides: it was leased, released again, changed
// or is not idle long enough. Nothing is recorded; the next sweep decides again.
var ErrNotIdle = errors.New("workspace is not idle past its grace")

// Sweep reclaims idle released Fresh workspaces. Within is the daemon's host
// lock and state update; Reclaim is the operator's reclaim with a condition
// that it checks in the transaction that records the bench reclaiming.
type Sweep struct {
	State   string
	Grace   time.Duration
	Now     func() time.Time
	Within  func(context.Context, func(workspace.Update) error) error
	Reclaim func(ctx context.Context, key string, eligible func(*model.Bench) error) error
	// Registered selects Fresh workspaces; nil reads the layout registry.
	Registered func(*model.Bench) (bool, error)
}

// Result is one key's outcome in one sweep.
type Result struct {
	Key, Status string // reclaimed, refused or failed
	Error       string `json:",omitempty"`
}

// MinimumGrace is the shortest grace the sweep accepts; activation bounds the
// installed setting to whole hours.
const MinimumGrace = time.Hour

func (s Sweep) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// released is the record shape the sweep may reclaim: a ready workspace with
// no writer, nothing pending or staged, no archive, transfer or repair, an
// allocation, and a complete checkpoint whose seal records its confirmation on
// the remote. The reclaim itself confirms the tree and remote again.
func released(b *model.Bench) bool {
	if b == nil || b.Workspace == nil {
		return false
	}
	w := b.Workspace
	return w.Phase == "ready" && b.Lease == nil && w.Pending == nil && w.Stage == nil && w.Checkpoint != nil && w.Checkpoint.Seal != "" &&
		w.Archive == nil && w.Transfer == nil && w.InitializationRepair == nil && b.ProjectID != 0
}

func (s Sweep) fresh(b *model.Bench) (bool, error) {
	if s.Registered != nil {
		return s.Registered(b)
	}
	return freshtree.Registered(s.State, b)
}

// eligible is the decision, made again inside the reclaim's transaction:
// released, Fresh, and idle since a recorded time older than the grace.
func (s Sweep) eligible(b *model.Bench) error {
	if !released(b) {
		return ErrNotIdle
	}
	if fresh, err := s.fresh(b); err != nil || !fresh {
		return errors.Join(ErrNotIdle, err)
	}
	since, ok := workspacepolicy.IdleSince(b)
	if !ok || !s.now().After(since.Add(s.Grace)) {
		return ErrNotIdle
	}
	return nil
}

// Select runs under the host lock. A released Fresh record without an idle
// time, from before the field existed, is given now: its grace starts at the
// first sweep that sees it, never in the past. It returns every key whose
// reclaim is already recorded (to resume) and every key idle past the grace.
func (s Sweep) Select(ctx context.Context) ([]string, error) {
	if s.Grace < MinimumGrace || s.Within == nil {
		return nil, errors.New("idle reclaim requires a grace of at least an hour and the daemon's state")
	}
	var keys []string
	err := s.Within(ctx, func(update workspace.Update) error {
		return update(func(rows benchstate.Database, _ *sql.Tx) error {
			keys = nil
			now := s.now().Format(time.RFC3339Nano)
			for key, b := range rows {
				if err := ctx.Err(); err != nil {
					return err
				}
				if b == nil || b.Workspace == nil {
					continue
				}
				if b.Workspace.Phase == "reclaiming" {
					keys = append(keys, key)
					continue
				}
				if !released(b) {
					continue
				}
				if fresh, err := s.fresh(b); err != nil || !fresh {
					continue // a legacy bench, or one the reclaim would refuse
				}
				if _, ok := workspacepolicy.IdleSince(b); !ok {
					b.Workspace.IdleSince = now
					continue
				}
				if s.eligible(b) == nil {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			return nil
		})
	})
	return keys, err
}

// Tick selects, then reclaims each selected key once, outside the selection's
// lock: each reclaim takes the host lock itself and decides again. A refused
// or failed reclaim is left for the next tick, so a busy bench is retried
// once per interval, never in a loop.
func (s Sweep) Tick(ctx context.Context) ([]Result, error) {
	if s.Reclaim == nil {
		return nil, errors.New("idle reclaim requires the reclaim route")
	}
	keys, err := s.Select(ctx)
	if err != nil {
		return nil, err
	}
	results := []Result{}
	for _, key := range keys {
		if err = ctx.Err(); err != nil {
			return results, err
		}
		r := Result{Key: key, Status: "reclaimed"}
		if err := s.Reclaim(ctx, key, s.eligible); errors.Is(err, ErrNotIdle) {
			r.Status = "refused"
		} else if err != nil {
			r.Status, r.Error = "failed", err.Error()
		}
		results = append(results, r)
	}
	return results, nil
}

// Run sweeps every interval until ctx ends. A zero grace (the setting is off)
// returns at once.
func (s Sweep) Run(ctx context.Context, interval time.Duration, report func(error)) {
	if s.Grace == 0 {
		return
	}
	if interval < time.Second || interval > 24*time.Hour {
		report(errors.New("invalid idle reclaim interval"))
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		results, err := s.Tick(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			report(err)
		}
		for _, r := range results {
			if r.Status == "failed" {
				report(fmt.Errorf("%s: %s", r.Key, r.Error))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
