package workspacerenew

import (
	"encoding/json"
	"io"
	"os"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
)

const Version = "superbench.workspace-renewal.v1"

// Result is the entire local output, shaped like stop_admin's grant: the new
// capability and the root path. It is written to stdout only, never logged.
type Result struct {
	Version       string `json:"version"`
	Key           string `json:"key"`
	BenchID       string `json:"bench_id"`
	Holder        string `json:"holder"`
	StopRequested bool   `json:"stop_requested"`
	GrantToken    string `json:"grant_token"`
	RootPath      string `json:"root_path"`
}

// uid is the caller's real identity: root only when both real and effective
// IDs are 0, so a setuid or partially dropped caller is never root.
func uid() uint32 {
	if os.Getuid() != 0 {
		return uint32(os.Getuid())
	}
	return uint32(os.Geteuid())
}

// Admin is `bench workspace renew_admin <key>`, the local administrative
// crossing. It refuses a non-root caller before opening or migrating state.
func Admin(store bench.Store, key string, stdout io.Writer) error {
	if uid() != 0 {
		return ErrRoot
	}
	repair, err := store.WorkspaceRepair()
	if err != nil {
		return err
	}
	b, err := Manager{Prep: repair.Prep}.Renew(key, 0)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(Result{Version: Version, Key: b.Key, BenchID: b.Workspace.ID, Holder: b.Lease.Holder, StopRequested: b.Lease.StopRequested, GrantToken: b.Lease.Token, RootPath: b.Path})
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}
