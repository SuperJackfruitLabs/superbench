package workspacepolicy

import (
	"context"
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// MaxGeneration bounds the generations of one key, as the bootstrap spec does.
const MaxGeneration = freshgeneration.Max

// NextGeneration is the generation a create on a reclaimed record, or on an
// unfinished create root retired, makes: 2 after a key's first bench, then
// one more each time.
func NextGeneration(reclaimed *model.Bench) uint32 {
	if reclaimed.Workspace.Generation == 0 {
		return 2
	}
	return reclaimed.Workspace.Generation + 1
}

// validateGeneration requires a first bench to name no generation, and a
// later one to name a generation of 2 or more and the different, reclaimed
// workspace it follows. Only a plain create makes one.
func validateGeneration(b *model.Bench) error {
	w := b.Workspace
	if w.Generation == 0 && w.Previous == "" {
		return nil
	}
	if !freshgeneration.Later(w.Generation, w.Previous, w.ID) {
		return errors.New("invalid workspace generation")
	}
	return nil
}

// Succeeds reports whether b may replace a reclaimed record with ID previous
// and generation: a new create of the next generation naming it, recorded
// before any allocation or tree. Validate fixes the rest of a materializing
// record's shape: its initialization owner, and no checkpoint, preparation,
// archive, transfer or repair. A reclaimed record changes in no other way.
func Succeeds(previous string, generation uint32, b *model.Bench) bool {
	if b == nil || b.Workspace == nil {
		return false
	}
	w := b.Workspace
	next := NextGeneration(&model.Bench{Workspace: &model.Workspace{Generation: generation}})
	return w.Phase == "materializing" && w.Generation == next && w.Previous == previous && b.ProjectID == 0
}

// Follow makes candidate, a new create's first record under reclaimed's key,
// the next generation after reclaimed.
func Follow(candidate, reclaimed *model.Bench) error {
	if reclaimed == nil || reclaimed.Workspace == nil || reclaimed.Workspace.Phase != "reclaimed" {
		return errors.New("a workspace follows only a reclaimed record under its own key")
	}
	return follow(candidate, reclaimed)
}

func follow(candidate, previous *model.Bench) error {
	if candidate == nil || candidate.Workspace == nil || candidate.Key != previous.Key {
		return errors.New("a workspace follows only a record under its own key")
	}
	candidate.Workspace.Generation, candidate.Workspace.Previous = NextGeneration(previous), previous.Workspace.ID
	return Validate(candidate)
}

// Unfinished reports a create that never finished: a valid materializing or
// initializing record with no run, no checkpoint, and no transfer or repair.
// (Validate already requires its initialization owner and, with a run, its
// stage.) A new create on its key follows it only once root proves its
// allocation retired (Prove).
func Unfinished(b *model.Bench) bool {
	if b == nil || b.Workspace == nil || Validate(b) != nil {
		return false
	}
	w := b.Workspace
	return (w.Phase == "materializing" || w.Phase == "initializing") && b.Lease.Execution == nil && w.Pending == nil && w.Checkpoint == nil && w.TransferredFrom == nil && w.InitializationRepair == nil
}

// Predecessor reports a record a new create on its key may follow: reclaimed,
// or unfinished, which Next follows only with root's proof.
func Predecessor(b *model.Bench) bool {
	return b != nil && b.Workspace != nil && b.Workspace.Phase == "reclaimed" || Unfinished(b)
}

// ErrUnretired refuses a create on a key whose unfinished create root has not
// proven retired. The record stays as it is.
var ErrUnretired = errors.New("workspace creation conflicts with an existing bench: its create never completed and root has not retired its allocation")

// Proof is a key's record that a new create may follow, as Prove found it.
// Only Prove makes one; the zero value follows nothing.
type Proof struct{ previous *model.Bench }

// Prove decides, before any plan, whether a new create may follow previous,
// its key's record: none needs nothing; a reclaimed record needs a Fresh
// daemon; an unfinished create also needs retired, root's read-only proof
// that its allocation was retired, never the daemon's own record alone.
func Prove(ctx context.Context, previous *model.Bench, fresh bool, retired func(context.Context, *model.Bench) error) (Proof, error) {
	if err := ctx.Err(); err != nil || previous == nil {
		return Proof{}, err
	}
	if !fresh {
		return Proof{}, errors.New("a key is created again only as a Fresh bench of its next generation")
	}
	if previous.Workspace == nil || previous.Workspace.Phase != "reclaimed" {
		if !Unfinished(previous) || retired == nil {
			return Proof{}, ErrUnretired
		}
		if err := retired(ctx, previous); err != nil {
			return Proof{}, errors.Join(ErrUnretired, err)
		}
	}
	return Proof{previous}, nil
}

// Follow makes candidate, a new create's first record, the next generation
// after the proven record; with none it leaves candidate the key's first.
func (p Proof) Follow(candidate *model.Bench) error {
	if p.previous == nil {
		return nil
	}
	return follow(candidate, p.previous)
}
