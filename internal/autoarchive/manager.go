// Package autoarchive applies explicit repository policy without releasing writers
// or deleting data. Core archive transactions remain the lifecycle authority.
package autoarchive

import (
	"context"
	"errors"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

var errIneligible = errors.New("repository has no confirmed branch-merged archive trigger")

type Result struct {
	Key, Status string
	Error       string `json:",omitempty"`
}
type Report struct {
	At      time.Time
	Results []Result
	Next    string
}
type Manager struct {
	Store bench.Store
	After string
}

// Tick bounds work and advances a round-robin cursor even across manual benches.
// A restart may repeat inspection; committed archive intents make that harmless.
func (m *Manager) Tick(ctx context.Context, limit int) (Report, error) {
	r := Report{At: time.Now().UTC(), Results: []Result{}}
	if limit < 1 || limit > 1000 {
		return r, errors.New("archive sweep limit must be between 1 and 1000")
	}
	benches, err := m.Store.Snapshot("")
	if err != nil {
		return r, err
	}
	for _, b := range benches {
		if b.Key <= m.After {
			continue
		}
		if err = ctx.Err(); err != nil {
			return r, err
		}
		r.Next = b.Key
		result := Result{Key: b.Key, Status: "skipped"}
		switch {
		case b.Workspace != nil:
			// Aggregate archival requires its whole-vector lifecycle.
		case b.Lease != nil:
			result.Status = "leased"
		case b.ArchivedAt != "" && (b.ArchiveMerge == nil || b.ArchiveFrozenAt != "" || b.ReapingDigest != "" || b.ReapedAt != ""):
			result.Status = "archived"
		default:
			_, err = m.archive(ctx, b.Key)
			switch {
			case err == nil:
				result.Status = "archived"
			case errors.Is(err, errIneligible):
				result.Status = "not eligible"
			case errors.Is(err, bench.ErrBusy):
				result.Status = "leased"
			default:
				result.Status, result.Error = "blocked", err.Error()
			}
		}
		r.Results = append(r.Results, result)
		if len(r.Results) == limit {
			m.After = r.Next
			return r, nil
		}
	}
	m.After, r.Next = "", ""
	return r, nil
}

func (m *Manager) archive(ctx context.Context, key string) (*bench.Bench, error) {
	return m.Store.ArchiveContext(ctx, key, archivepolicy.DefaultGrace, func(ctx context.Context, b *bench.Bench) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A new task starts at an already integrated base commit. Reachability
		// alone is not task completion; require an actual subsequent checkpoint.
		if b.CreatedFrom == "" || b.Checkpoint == "" || b.Checkpoint == b.CreatedFrom || b.ArchiveUnmerged == nil {
			return errIneligible
		}
		// Only committed policy can authorize an automatic transition. Compare
		// physical bytes too: Git index flags can conceal checkout modifications.
		c, err := setup.At(ctx, b.Mirror, b.Checkpoint)
		if err != nil {
			return err
		}
		if c == nil || c.ArchiveWhen() != "branch-merged" {
			return errIneligible
		}
		actual, err := setup.Read(b.Path)
		if err != nil {
			return err
		}
		if actual == nil || actual.Digest != c.Digest {
			return errors.New("checkout archive policy differs from its checkpoint")
		}
		if b.ArchiveUnmerged.Checkpoint != b.Checkpoint || b.ArchiveUnmerged.ConfigDigest != c.Digest {
			return errIneligible
		}
		mirrors := repository.Mirrors{State: m.Store.State, Data: m.Store.Data, Timeout: m.Store.MirrorTimeout}
		evidence, err := mirrors.Merged(ctx, b.Repo, repository.Checkout{Path: b.Path, Branch: b.Branch, Mirror: b.Mirror}, b.Checkpoint)
		if err != nil {
			return err
		}
		if evidence.Commit == "" || evidence.Branch != b.ArchiveUnmerged.TargetBranch {
			return errIneligible
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		b.ArchiveMerge = &model.ArchiveMergeEvidence{Checkpoint: b.Checkpoint, ConfigDigest: c.Digest,
			TargetBranch: evidence.Branch, TargetCommit: evidence.Commit, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		return nil
	})
}

func (m *Manager) Run(ctx context.Context, interval time.Duration, onReport func(Report, error)) {
	if interval < time.Second || interval > 24*time.Hour {
		if onReport != nil {
			onReport(Report{}, errors.New("invalid archive sweep interval"))
		}
		return
	}
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		r, err := m.Tick(ctx, 100)
		if onReport != nil && ctx.Err() == nil {
			onReport(r, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
