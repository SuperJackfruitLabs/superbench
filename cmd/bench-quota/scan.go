package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/projectscan"
)

// scanProjects is the direct-root operator scan: one JSON line per allocated
// bench, and an error when any tree is outside its quota project. Run it at
// install, upgrade and before turning on Fresh; it changes nothing.
func scanProjects(policy string, output io.Writer) error {
	if os.Getuid() != 0 || os.Geteuid() != 0 || os.Getenv("SUDO_UID") != "" {
		return fmt.Errorf("scan-projects requires a direct root caller")
	}
	p, err := hoststorage.Load(policy)
	if err != nil {
		return err
	}
	results, err := projectscan.Scan(p.Root, p.Ledger)
	encoder := json.NewEncoder(output)
	for _, r := range results {
		if e := encoder.Encode(r); e != nil {
			return e
		}
	}
	return err
}
