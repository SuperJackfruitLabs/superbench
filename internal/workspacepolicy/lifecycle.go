package workspacepolicy

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/checkpointset"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Manifest produces the canonical vector without requiring its Digest first.
// This lets producers compute SHA256(json.Marshal(manifest)) before persistence.
func Manifest(b *model.Bench, c *model.WorkspaceCheckpoint) (checkpointset.Manifest, error) {
	invalid := errors.New("invalid aggregate checkpoint vector")
	if b == nil || b.Workspace == nil || c == nil || len(c.Members) != len(b.Workspace.Members) {
		return checkpointset.Manifest{}, invalid
	}
	w := b.Workspace
	m := checkpointset.Manifest{Version: checkpointset.Version, BenchID: w.ID, GenerationID: c.GenerationID, CheckpointRepository: w.CheckpointRepository}
	if c.Previous != "" {
		previous := c.Previous
		m.PreviousManifestCommit = &previous
	}
	for i, v := range c.Members {
		member := w.Members[i]
		if v.Name != member.Name {
			return checkpointset.Manifest{}, invalid
		}
		pin, err := checkpointset.PinRef(w.ID, c.GenerationID, v.Name)
		if err != nil {
			return checkpointset.Manifest{}, invalid
		}
		m.Members = append(m.Members, checkpointset.Member{Name: v.Name, Repository: member.Repository, TaskRef: "refs/heads/" + member.Branch, PinRef: pin, ObjectFormat: member.ObjectFormat, Commit: v.Commit})
	}
	if err := m.Validate(); err != nil {
		return checkpointset.Manifest{}, invalid
	}
	return m, nil
}
func validateCheckpoint(b *model.Bench, c *model.WorkspaceCheckpoint, complete bool) error {
	invalid := errors.New("invalid aggregate checkpoint evidence")
	m, err := Manifest(b, c)
	if err != nil {
		return invalid
	}
	raw, err := json.Marshal(m)
	if err != nil || !digestPattern.MatchString(c.Digest) || c.Digest != checkpointset.Digest(raw) {
		return invalid
	}
	if !complete {
		if c.Seal != "" {
			return invalid
		}
		return nil
	}
	for _, v := range m.Members {
		if v.Name == m.CheckpointRepository {
			if len(c.Seal) != len(v.Commit) || strings.ToLower(c.Seal) != c.Seal || !model.ValidCommit(c.Seal) {
				return invalid
			}
			return nil
		}
	}
	return invalid
}
func validatePreparation(b *model.Bench) error {
	invalid := errors.New("invalid aggregate preparation proof")
	w := b.Workspace
	if w.Phase == "materializing" && len(w.Preparation) != 0 {
		return invalid
	}
	if w.Phase == "prepared" && len(w.Preparation) != len(w.Members) {
		return invalid
	}
	known := map[string]bool{}
	for _, m := range w.Members {
		known[m.Name] = true
	}
	for i, p := range w.Preparation {
		if !known[p.Name] || i > 0 && w.Preparation[i-1].Name >= p.Name || !digestPattern.MatchString(p.Digest) || p.RunID != "" && !identity.MatchString(p.RunID) {
			return invalid
		}
		if p.RunID != "" && (w.Phase == "initializing" || w.Phase == "prepared") {
			found := false
			if b.Lease != nil {
				for _, id := range b.Lease.RetainedRuns {
					found = found || id == p.RunID
				}
				if e := b.Lease.Execution; e != nil && e.Phase == "stopped" && e.ID == p.RunID {
					found = true
				}
			}
			if !found {
				return invalid
			}
		}
	}
	return nil
}

func validateCheckpoints(b *model.Bench) error {
	invalid := errors.New("invalid aggregate checkpoint state")
	w := b.Workspace
	if w.Phase == "materializing" && (w.Pending != nil || w.Checkpoint != nil) {
		return invalid
	}
	if w.Checkpoint != nil {
		if err := validateCheckpoint(b, w.Checkpoint, true); err != nil {
			return err
		}
	}
	if w.Pending != nil {
		if err := validateCheckpoint(b, w.Pending, false); err != nil {
			return err
		}
		previous := ""
		if w.Checkpoint != nil {
			previous = w.Checkpoint.Seal
			if w.Pending.GenerationID == w.Checkpoint.GenerationID {
				return invalid
			}
		}
		if w.Pending.Previous != previous || b.Lease == nil || b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
			return invalid
		}
	}
	if w.Phase == "ready" && w.Checkpoint == nil {
		return invalid
	}
	return nil
}
func boundedText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func physicalPath(s string) bool {
	return boundedText(s, 4096) && filepath.IsAbs(s) && filepath.Clean(s) == s && s != "/"
}
func validateLease(b *model.Bench) error {
	invalid := errors.New("invalid aggregate initialization or writer lease")
	l := b.Lease
	w := b.Workspace
	if stage := w.Stage; stage != nil {
		known := false
		for _, m := range w.Members {
			known = known || m.Name == stage.Name
		}
		if !known || !digestPattern.MatchString(stage.Digest) || l == nil || l.Execution == nil || w.Phase == "prepared" || w.Phase == "materializing" {
			return invalid
		}
	}
	if l != nil && l.Execution != nil && w.Phase == "initializing" && w.Stage == nil {
		return invalid
	}
	if l == nil {
		if (w.Phase == "ready" || Reclaim(w.Phase) || w.Archive != nil) && w.Pending == nil {
			return nil
		}
		return invalid
	}
	if !token.MatchString(l.Token) || !boundedText(l.Holder, 200) || w.Phase != "ready" && l.Holder != "initialization" {
		return invalid
	}
	if l.Continuation != "" && !identity.MatchString(l.Continuation) || l.Continuation == "" && w.Phase != "ready" && !l.StopRequested {
		return invalid
	}
	if len(l.RetainedRuns) > model.WorkspaceRetainedRuns {
		return invalid
	}
	seen := map[string]bool{}
	for _, id := range l.RetainedRuns {
		if !identity.MatchString(id) || seen[id] || l.Execution != nil && id == l.Execution.ID {
			return invalid
		}
		seen[id] = true
	}
	if w.Phase == "materializing" && (l.Execution != nil || len(l.RetainedRuns) != 0 || l.StopRequested && l.Continuation != "") {
		return invalid
	}
	if e := l.Execution; e != nil {
		if !identity.MatchString(e.ID) || !physicalPath(e.Root) || !physicalPath(e.Log) || !boundedText(e.BootID, 128) || !boundedText(e.HostID, 128) || e.PID < 0 || e.LogLimitBytes < 1024 || e.LogLimitBytes > 1<<30 {
			return invalid
		}
		if e.Phase != "starting" && e.Phase != "running" && e.Phase != "stopped" || e.Phase == "running" && e.PID == 0 {
			return invalid
		}
	}
	return nil
}

// ValidateSchema14 permits only the staged aggregate shape that schema14 owned.
func ValidateSchema14(b *model.Bench) error {
	if err := ValidateSchema17(b); err != nil {
		return err
	}
	w := b.Workspace
	l := b.Lease
	if w.RecoveredFrom != nil || w.Phase != "materializing" && w.Phase != "initializing" || w.Pending != nil || w.Checkpoint != nil || w.Stage != nil || len(w.Preparation) != 0 || l == nil || l.Execution != nil || len(l.RetainedRuns) != 0 || l.StopRequested || !identity.MatchString(l.Continuation) {
		return errors.New("aggregate does not match historical staging schema")
	}
	return nil
}

// ValidateSchema15 preserves the former full-proof ready representation during
// migration, without allowing schema16 acquisition states in an older schema.
func ValidateSchema15(b *model.Bench) error {
	if err := ValidateSchema17(b); err != nil {
		return err
	}
	if b.Workspace.RecoveredFrom != nil || b.Workspace.Phase == "ready" && len(b.Workspace.Preparation) != len(b.Workspace.Members) {
		return errors.New("aggregate does not match historical lifecycle schema")
	}
	return nil
}

// ValidateSchema16 excludes recovery provenance that only schema17 can own.
func ValidateSchema16(b *model.Bench) error {
	if err := ValidateSchema17(b); err != nil {
		return err
	}
	if b.Workspace.RecoveredFrom != nil {
		return errors.New("aggregate does not match historical ownership schema")
	}
	return nil
}

// ValidateSchema17 excludes aggregate archive observations and lifecycle intent.
func ValidateSchema17(b *model.Bench) error {
	if err := ValidateSchema19(b); err != nil {
		return err
	}
	w := b.Workspace
	if w.Archive != nil || w.Unmerged != nil || w.Phase == "archived" || w.Phase == "reaping" || w.Phase == "reaped" {
		return errors.New("aggregate does not match historical recovery schema")
	}
	return nil
}
