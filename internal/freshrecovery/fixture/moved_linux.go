//go:build linux && freshstoragefixture

package fixture

import (
	"fmt"
	"path/filepath"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/lowerfixture"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadbootstrap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/repository"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacebootstrap"
)

// movedBranch is the card branch the moved cases ask for. The daemon mirror
// holds it only as refs/remotes/origin/card/moved, as its refresh leaves a
// branch the remote has and the mirror has no local branch for.
const movedBranch = "card/moved"

// advanceMoved commits file on top of each member's current source, as the
// daemon account, and points the mirror's remote-tracking card branch at it.
// It returns the members with the new commits as their sources.
func (e *env) advanceMoved(members []payloadbootstrap.Member, file string) []payloadbootstrap.Member {
	t := e.t
	t.Helper()
	moved := append([]payloadbootstrap.Member{}, members...)
	for k, m := range moved {
		mirror := repository.Path(e.p.Root, m.Repository)
		heads := lowerfixture.DaemonGit(t, mirror, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
		blob := strings.TrimSpace(lowerfixture.DaemonGitInput(t, mirror, file+"-"+m.Name+"\n", "hash-object", "-w", "--stdin"))
		entries := lowerfixture.DaemonGit(t, mirror, "ls-tree", m.Source) + fmt.Sprintf("100644 blob %s\t%s\n", blob, file)
		tree := strings.TrimSpace(lowerfixture.DaemonGitInput(t, mirror, entries, "mktree"))
		commit := strings.TrimSpace(lowerfixture.DaemonGit(t, mirror, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-p", m.Source, "-m", file))
		lowerfixture.DaemonGit(t, mirror, "update-ref", "refs/remotes/origin/"+movedBranch, commit)
		if got := strings.TrimSpace(lowerfixture.DaemonGit(t, mirror, "for-each-ref", "--contains", commit, "--format=%(refname)")); got != "refs/remotes/origin/"+movedBranch {
			t.Fatalf("the moved source must be reachable only from the remote-tracking branch: %q", got)
		}
		if lowerfixture.DaemonGit(t, mirror, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") != heads {
			t.Fatal("advancing the remote-tracking branch moved a local branch")
		}
		moved[k].Branch, moved[k].Source = movedBranch, commit
	}
	return moved
}

// assertCheckedOut requires each member's checkout to be the moved branch at
// exactly its source, with file from that source and none of the mirror's
// remote-tracking branches, read by Git as the payload account that owns it.
func (e *env) assertCheckedOut(b bench, file string) {
	t := e.t
	t.Helper()
	args := []string{movedBranch}
	for _, m := range b.spec.Members {
		assertBytes(t, filepath.Join(b.allocation.Path, m.Name, file), []byte(file+"-"+m.Name+"\n"))
		args = append(args, m.Name, m.Source)
	}
	e.payloadShell(b.allocation.Path, `
branch=$1; shift
while [ "$#" -gt 0 ]; do
  test "$(git -C "$1" symbolic-ref --quiet HEAD)" = "refs/heads/$branch"
  test "$(git -C "$1" rev-parse --verify 'HEAD^{commit}')" = "$2"
  test -z "$(git -C "$1" for-each-ref "refs/remotes/origin/$branch")"
  test -z "$(git -C "$1" status --porcelain=v1 --untracked-files=all)"
  shift 2
done
`, args...)
}

// movedBranches proves, on real XFS through the real Bootstrap, a Fresh create
// and a reclaimed key's next generation from a branch that has moved past
// main and that the mirror holds only under refs/remotes/origin/*.
func (e *env) movedBranches() {
	t := e.t
	key := "moved"
	s := e.template.Spec
	s.Key, s.ID, s.QuotaBytes = key, hash("moved-bid:" + key)[:32], 0
	s.Members = e.advanceMoved(s.Members, "moved-first.txt")
	first, err := e.next(s, "moved")
	if err != nil {
		t.Fatal("a Fresh create from a moved branch:", err)
	}
	e.assertCheckedOut(first, "moved-first.txt")
	t.Log("LOWER_FRESH_CREATE_FROM_MOVED_BRANCH_PASS")

	// The first generation's work reached the remote and the mirror's refresh:
	// the branch moves again, and the key is reclaimed and created again.
	got, err := e.reclaim(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	e.assertReclaimed(first, got)
	second := successor(first, "moved-second")
	second.Members = e.advanceMoved(first.spec.Members, "moved-second.txt")
	policy, err := payloadexec.LoadFixedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	record := &model.Bench{Key: key, Workspace: &model.Workspace{ID: second.ID, Generation: second.Generation, Previous: second.Previous}}
	if err = workspacebootstrap.MoveLayoutAside(policy.StateRoot, record); err != nil {
		t.Fatal(err)
	}
	b, err := e.next(second, "moved-recreate")
	if err != nil {
		t.Fatal("the next generation from a moved branch:", err)
	}
	e.assertCheckedOut(b, "moved-second.txt")
	assertBytes(t, filepath.Join(b.allocation.Path, b.spec.Members[0].Name, "moved-first.txt"), []byte("moved-first.txt-"+b.spec.Members[0].Name+"\n"))
	if p := e.intentPhase(payloadbootstrap.Stem(second)); p != "completed" {
		t.Fatalf("second generation intent %s", p)
	}
	t.Log("LOWER_RECREATE_FROM_MOVED_BRANCH_PASS")
	if got, err = e.reclaim(t.Context(), b); err != nil {
		t.Fatal("moved second generation reclaim:", err)
	}
	e.assertReclaimed(b, got)
}
