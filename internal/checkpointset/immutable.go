package checkpointset

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
)

// VerifyImmutable checks the exact registered generation against its remote
// metadata history and immutable pins. Mutable task refs may have advanced.
// This is evidence for retiring an independently fenced transferred source,
// never authority to checkpoint or release an ordinary workspace writer.
// The caller retains the host lock and verifies transfer and checkout identity.
func (c Coordinator) VerifyImmutable(ctx context.Context, expected Manifest, seal, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(expected)
	if err != nil || len(raw) > MaxRaw {
		return errors.New("registered checkpoint manifest exceeds bounds")
	}
	got, err := c.Recover(ctx, coordinatorMember(expected).Repository, expected.BenchID, seal, digest)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, expected) {
		return errors.New("immutable checkpoint differs from the registered generation")
	}
	return nil
}
