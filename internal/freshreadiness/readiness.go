// Package freshreadiness resolves what must be installed before benchd turns on
// fresh_payload: the service identity and the indexed quota operation journal.
// activation.Check calls both before benchd opens its socket.
package freshreadiness

import (
	"fmt"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

// StoragePolicy is the fixed root-owned policy the quota helper reads.
const StoragePolicy = "/etc/superbench/storage.json"

// IndexedJournal is the only operation journal Fresh payload bootstrap accepts.
const IndexedJournal = "indexed-v2"

// Identity resolves the installed payload policy and service identity.
func Identity() error {
	_, err := serviceidentity.ResolveService()
	return err
}

// Storage refuses unless the installed storage policy selects the indexed
// journal. The daemon cannot read the private ledger, so this checks the policy
// only; the helper itself refuses an indexed policy whose ledger was never
// initialized.
func Storage() error { return indexed(hoststorage.Load(StoragePolicy)) }

func indexed(p hoststorage.Policy, err error) error {
	if err != nil {
		return fmt.Errorf("read %s: %w", StoragePolicy, err)
	}
	if p.OperationJournal != IndexedJournal {
		return fmt.Errorf("%s does not set \"OperationJournal\": %q, so every Fresh create would stop at materializing; "+
			"a fresh host gets it from the installer's --storage (docs/pilot-installation.md)", StoragePolicy, IndexedJournal)
	}
	return nil
}
