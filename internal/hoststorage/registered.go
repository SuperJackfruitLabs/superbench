package hoststorage

import (
	"errors"
	"os"
	"path/filepath"
)

var ErrFreshAllocation = errors.New("registered payload requires an exclusively fresh allocation")
var ErrRegisteredAllocation = errors.New("registered payload allocation requires its retained bootstrap authority")

// A retained bootstrap intent permanently excludes legacy Apply, even before
// publication or after a partial transfer. Neither a missing registration nor a
// failed materialization grants permission to chown an existing allocation.
func (p Policy) refuseRegisteredAllocation(key string) error {
	path := filepath.Join(p.Ledger, "payload-bootstrap", key+".json")
	if _, err := os.Lstat(path); err == nil {
		return ErrRegisteredAllocation
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(ErrRegisteredAllocation, err)
	}
	return nil
}
