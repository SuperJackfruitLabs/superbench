// Package freshreadiness resolves what must be installed before benchd turns on
// fresh_payload: the service identity and a ready indexed quota operation
// journal. activation.Check calls both before benchd opens its socket.
package freshreadiness

import (
	"context"
	"fmt"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotahelper"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

// StoragePolicy is the fixed root-owned policy the quota helper reads.
const StoragePolicy = "/etc/superbench/storage.json"

// QuotaHelper is the fixed installed helper benchd may run through sudo.
const QuotaHelper = "/usr/local/libexec/bench-quota"

// IndexedJournal is the only operation journal Fresh payload bootstrap accepts.
const IndexedJournal = "indexed-v2"

// Identity resolves the installed payload policy and service identity.
func Identity() error {
	_, err := serviceidentity.ResolveService()
	return err
}

// Storage refuses unless the installed storage policy selects the indexed
// journal and the helper can open that journal. benchd cannot read the private
// ledger, so it asks the helper's read-only operation-info, which on an indexed
// policy answers only when initialize-indexed wrote its ready record. A
// half-finished initialization therefore fails here, at start.
func Storage() error {
	p, err := hoststorage.Load(StoragePolicy)
	return ready(p, err, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := quotahelper.OperationInfo(ctx, quotahelper.Config{Command: QuotaHelper, Data: p.Root})
		return err
	})
}

func ready(p hoststorage.Policy, err error, open func() error) error {
	if err != nil {
		return fmt.Errorf("read %s: %w", StoragePolicy, err)
	}
	if p.OperationJournal != IndexedJournal {
		return fmt.Errorf("%s does not set \"OperationJournal\": %q, so every Fresh create would stop at materializing; "+
			"a fresh host gets it from the installer's --storage (docs/pilot-installation.md)", StoragePolicy, IndexedJournal)
	}
	if err = open(); err != nil {
		return fmt.Errorf("the indexed quota journal is not ready (initialize-indexed did not finish, or its ready record does not match the mounted storage; see docs/pilot-installation.md): %w", err)
	}
	return nil
}
