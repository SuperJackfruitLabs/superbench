package repository

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// MergeEvidence identifies the observed remote default branch and its commit.
// Branch is a full refs/heads/... name. This is snapshot evidence, not a lock on
// future remote updates; callers must bind it to their local archive intent.
type MergeEvidence struct{ Branch, Commit string }

// MergeObservation distinguishes a verified unmerged task checkpoint from an
// absent or moved task ref, which supplies no authority for either conclusion.
type MergeObservation struct {
	Branch, Commit string
	Merged         bool
}

var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Merged refreshes the registered checkout's mirror and proves only ancestry
// merges. Deleted task branches, moved task tips, and squash-only equivalence
// are not merge evidence. The caller serializes shared mirror mutations.
func (m Mirrors) Merged(ctx context.Context, repo string, c Checkout, checkpoint string) (MergeEvidence, error) {
	observation, err := m.ObserveMerge(ctx, repo, c, checkpoint)
	if err != nil || !observation.Merged {
		return MergeEvidence{}, err
	}
	return MergeEvidence{Branch: observation.Branch, Commit: observation.Commit}, nil
}

// ObserveMerge returns a populated observation only while the remote task ref
// still identifies this checkpoint and the default advertisement matches the
// refreshed mirror. Its caller serializes shared mirror mutations.
func (m Mirrors) ObserveMerge(ctx context.Context, repo string, c Checkout, checkpoint string) (MergeObservation, error) {
	var none MergeObservation
	if !commitID.MatchString(checkpoint) {
		return none, errors.New("invalid merge checkpoint")
	}
	ctx, cancel, err := m.deadline(ctx)
	if err != nil {
		return none, err
	}
	defer cancel()
	if err = m.RefreshCheckout(ctx, repo, c); err != nil {
		return none, err
	}
	advertised, err := git(ctx, c.Mirror, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return none, err
	}
	lines := strings.Split(strings.TrimSpace(advertised), "\n")
	if len(lines) != 2 {
		return none, errors.New("remote default branch is missing or ambiguous")
	}
	symbolic, head := strings.Fields(lines[0]), strings.Fields(lines[1])
	if len(symbolic) != 3 || symbolic[0] != "ref:" || symbolic[2] != "HEAD" || !strings.HasPrefix(symbolic[1], "refs/heads/") || len(head) != 2 || head[1] != "HEAD" || !commitID.MatchString(head[0]) {
		return none, errors.New("invalid remote default branch advertisement")
	}
	target := symbolic[1]
	if _, err = git(ctx, c.Mirror, "check-ref-format", target); err != nil {
		return none, err
	}
	if target == "refs/heads/"+c.Branch {
		return none, nil
	}
	fetched, err := git(ctx, c.Mirror, "rev-parse", "--verify", "refs/remotes/origin/"+strings.TrimPrefix(target, "refs/heads/")+"^{commit}")
	if err != nil {
		return none, err
	}
	if strings.TrimSpace(fetched) != head[0] {
		return none, errors.New("remote default branch changed after mirror refresh")
	}
	taskRef := "refs/heads/" + c.Branch
	task, err := git(ctx, c.Mirror, "ls-remote", "--refs", "origin", taskRef)
	if err != nil {
		return none, err
	}
	if strings.TrimSpace(task) == "" {
		return none, nil
	}
	fields := strings.Fields(task)
	if len(fields) != 2 || fields[1] != taskRef || !commitID.MatchString(fields[0]) {
		return none, errors.New("invalid remote task branch advertisement")
	}
	if fields[0] != checkpoint {
		return none, nil
	}
	_, err = git(ctx, c.Mirror, "--no-replace-objects", "merge-base", "--is-ancestor", checkpoint, head[0])
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return MergeObservation{Branch: target, Commit: head[0]}, nil
		}
		return none, err
	}
	return MergeObservation{Branch: target, Commit: head[0], Merged: true}, nil
}
