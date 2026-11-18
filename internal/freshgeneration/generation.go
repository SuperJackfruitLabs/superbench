// Package freshgeneration names the generations of one bench key. A key's
// first Fresh bench is generation 0 (it names none); once it is reclaimed, a
// create on the key makes generation 2, then 3, and so on. Each generation has
// its own workspace ID, bootstrap intent, controls and quota project; an
// earlier one's evidence stays where it is and is never reused.
package freshgeneration

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// Max bounds the generations of one key.
const Max = 1 << 20

var identity = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ErrGeneration refuses a generation that does not follow its predecessor.
var ErrGeneration = errors.New("bench generation does not follow its reclaimed predecessor")

// Later reports a valid later generation: 2 to Max, naming the different
// workspace ID it follows.
func Later(generation uint32, previous, id string) bool {
	return generation >= 2 && generation <= Max && identity.MatchString(previous) && previous != id
}

// Before is the generation a later one follows: 0 (the first) for 2.
func Before(generation uint32) uint32 {
	if generation <= 2 {
		return 0
	}
	return generation - 1
}

// Stem names a generation's retained files: the key itself for the first
// generation, whose names are unchanged, and KEY@N after it. A key never
// contains "@", so no stem names another key's files.
func Stem(key string, generation uint32) string {
	if generation == 0 {
		return key
	}
	return key + "@" + strconv.FormatUint(uint64(generation), 10)
}

// Prior is what a later generation reads of its predecessor's retained
// bootstrap intent.
type Prior struct {
	Key, ID    string
	Generation uint32
	Phase      string
	Authority  string // the storage authority its operation recorded
	Path       string // its recorded leaf
	ProjectID  uint32
}

// Follows requires prior to be the reclaimed generation right before
// generation of key, with workspace ID previous, under the same storage
// authority and at the same leaf root, holding a Fresh project.
func Follows(prior Prior, key, previous string, generation uint32, authority, root string) error {
	if generation == 0 || prior.Key != key || prior.ID != previous || prior.Generation != Before(generation) || prior.Phase != "reclaimed" ||
		prior.Authority != authority || prior.Path != root || prior.ProjectID < 10000 {
		return ErrGeneration
	}
	return nil
}

// LayoutAbsent refuses, before any effect, while the daemon's layout registry
// in state still publishes a layout under key. Publication creates a key's
// registration exclusively, so a later generation's would be refused only
// after its tree was transferred. The daemon moves the reclaimed generation's
// registration aside first. This is an early check; publication is the fence.
func LayoutAbsent(state, key string) error {
	_, err := os.Lstat(filepath.Join(state, "payload-layouts", key+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.Join(ErrGeneration, errors.New("the reclaimed generation's layout registration is still published"), err)
}
