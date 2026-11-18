package workspacepolicy

import (
	"errors"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshgeneration"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

// MaxGeneration bounds the generations of one key, as the bootstrap spec does.
const MaxGeneration = freshgeneration.Max

// NextGeneration is the generation a create on a reclaimed record makes: 2
// after a key's first bench, then one more each time.
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
