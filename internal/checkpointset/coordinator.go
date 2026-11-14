package checkpointset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Coordinator is called under the bench/mirror lock. Gate proves the one held
// lease, stopped writers and exact target vector; a nil gate never grants work.
// State is private authoritative storage outside bounded checkout data.
type Coordinator struct {
	State   string
	Mirrors map[string]string
	Gate    func(context.Context, Manifest) error
}
type Seal struct{ BenchID, Commit, Digest string }

func coordinatorMember(m Manifest) Member {
	for _, v := range m.Members {
		if v.Name == m.CheckpointRepository {
			return v
		}
	}
	return Member{}
}
func previous(m Manifest) string {
	if m.PreviousManifestCommit != nil {
		return *m.PreviousManifestCommit
	}
	return ""
}
func (c Coordinator) gate(ctx context.Context, m Manifest) error {
	if c.Gate == nil {
		return errors.New("checkpoint completion requires lifecycle authority")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if c.Gate(ctx, m) != nil {
		return errors.New("checkpoint lifecycle authority refused")
	}
	return nil
}
func (c Coordinator) Complete(ctx context.Context, m Manifest) (Seal, error) {
	var none Seal
	if e := m.Validate(); e != nil {
		return none, e
	}
	// Bound the complete operation even for a caller with a background context.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if e := c.gate(ctx, m); e != nil {
		return none, e
	}
	unlock, e := c.lock(ctx, m.BenchID)
	if e != nil {
		return none, e
	}
	defer unlock()
	raw, e := json.Marshal(m)
	if e != nil || len(raw) > MaxRaw {
		return none, errors.New("checkpoint manifest cannot be encoded")
	}
	paths := map[string]string{}
	for _, v := range m.Members {
		path, e := c.mirror(ctx, v.Repository)
		if e != nil {
			return none, e
		}
		paths[v.Name] = path
		format, e := gitText(ctx, path, "rev-parse", "--show-object-format")
		if e != nil || format != v.ObjectFormat {
			return none, errors.New("checkpoint object format differs")
		}
		kind, e := gitText(ctx, path, "cat-file", "-t", v.Commit)
		if e != nil || kind != "commit" {
			return none, errors.New("checkpoint target commit unavailable")
		}
	}
	j, e := c.journal(m, raw)
	if e != nil {
		return none, e
	}
	owner := coordinatorMember(m)
	path := paths[owner.Name]
	meta, _ := MetadataRef(m.BenchID)
	tip, e := remoteRef(ctx, path, meta)
	if e != nil {
		return none, e
	}
	if previous(m) != "" {
		if tip == "" {
			return none, errors.New("previous checkpoint metadata branch is missing")
		}
		if _, e = gitText(ctx, path, "fetch", "--no-tags", "--no-write-fetch-head", "origin", meta); e != nil {
			return none, e
		}
		if _, e = gitText(ctx, path, "merge-base", "--is-ancestor", previous(m), tip); e != nil {
			return none, errors.New("previous checkpoint is outside metadata history")
		}
		prior, e := readManifest(ctx, path, previous(m), "")
		if e != nil || !sameLayout(prior, m) || prior.GenerationID == m.GenerationID {
			return none, errors.New("previous checkpoint seal differs")
		}
	}
	candidate, e := sealCommit(ctx, path, m, j)
	if e != nil {
		return none, e
	}
	if tip != previous(m) && tip != candidate {
		return none, errors.New("checkpoint metadata head differs from predecessor")
	}
	// Detect every known immutable-pin conflict before publishing any task ref.
	for _, v := range m.Members {
		pin, err := remoteRef(ctx, paths[v.Name], v.PinRef)
		if err != nil {
			return none, err
		}
		if pin != "" && pin != v.Commit {
			return none, errors.New("checkpoint pin already names another commit")
		}
	}
	for _, v := range m.Members {
		if e = c.gate(ctx, m); e != nil {
			return none, e
		}
		path := paths[v.Name]
		if e = push(ctx, path, v.TaskRef, v.Commit); e != nil {
			return none, e
		}
		pin, e := remoteRef(ctx, path, v.PinRef)
		if e != nil {
			return none, e
		}
		if pin != "" && pin != v.Commit {
			return none, errors.New("checkpoint pin already names another commit")
		}
		if pin == "" {
			if e = createPin(ctx, path, v.PinRef, v.Commit); e != nil {
				return none, e
			}
		}
	}
	for _, v := range m.Members {
		if e = confirm(ctx, paths[v.Name], v.TaskRef, v.Commit); e != nil {
			return none, e
		}
		if e = confirm(ctx, paths[v.Name], v.PinRef, v.Commit); e != nil {
			return none, e
		}
	}
	if e = c.gate(ctx, m); e != nil {
		return none, e
	}
	if e = push(ctx, path, meta, candidate); e != nil {
		return none, e
	}
	return Seal{BenchID: m.BenchID, Commit: candidate, Digest: j.Digest}, nil
}
func sealCommit(ctx context.Context, path string, m Manifest, j intent) (string, error) {
	blob, e := runGit(ctx, path, j.Raw, nil, "hash-object", "-w", "--stdin")
	if e != nil {
		return "", e
	}
	tree, e := runGit(ctx, path, []byte("100644 blob "+strings.TrimSpace(blob)+"\tcheckpoint.json\n"), nil, "mktree")
	if e != nil {
		return "", e
	}
	args := []string{"commit-tree", strings.TrimSpace(tree), "-m", "SuperBench checkpoint generation"}
	if previous(m) != "" {
		args = append(args, "-p", previous(m))
	}
	commit, e := runGit(ctx, path, nil, []string{"GIT_AUTHOR_DATE=" + j.Prepared, "GIT_COMMITTER_DATE=" + j.Prepared}, args...)
	return strings.TrimSpace(commit), e
}

// A bench fixes repository identity/layout for its sealed history. Changing the
// participants or coordinator requires a separately reviewed migration.
func sameLayout(a, b Manifest) bool {
	if a.BenchID != b.BenchID || a.CheckpointRepository != b.CheckpointRepository || len(a.Members) != len(b.Members) {
		return false
	}
	for i, old := range a.Members {
		next := b.Members[i]
		if old.Name != next.Name || old.Repository != next.Repository || old.TaskRef != next.TaskRef || old.ObjectFormat != next.ObjectFormat {
			return false
		}
	}
	return true
}
