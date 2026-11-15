// Package workspaceops orchestrates aggregate API operations. It does not own
// transport authentication, request receipts, or wire response projections.
package workspaceops

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/archivepolicy"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
)

var (
	ErrInvalid     = errors.New("invalid workspace operation")
	ErrConflict    = errors.New("workspace operation conflicts with current state")
	ErrUnavailable = errors.New("workspace operation unavailable")
)

type Preparation struct{ Name, Status string }
type Result struct {
	Workspace    *model.Bench
	GrantToken   string
	RootPath     bool
	Execution    *execution.Execution
	ExitCode     *int
	LogTruncated *bool
	Preparation  []Preparation
}
type Manager struct{ Store bench.Store }

// Preflight rejects kind, selector, capability and output-bound failures before
// a service reserves growth receipts. It never allocates or mutates state.
func (m Manager) Preflight(r protocolv2.Request) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return ErrInvalid
	}
	if _, err := protocolv2.Parse(raw); err != nil {
		return ErrInvalid
	}
	if protocolv2.ReadOnly(r.Operation) {
		return nil
	}
	creating := r.Operation == "create" || r.Operation == "recover"
	growth := protocolv2.Growth(r.Operation)
	root := filepath.Join(m.Store.Data, "benches", r.Key)
	if growth && (!utf8.ValidString(root) || len(root) > 4096) {
		return ErrInvalid
	}
	if creating {
		spec := creationSpec(r)
		if r.Operation == "create" {
			err = workspace.ValidateStage("preflight", spec)
		} else {
			err = workspace.ValidateRecovery("preflight", recoverySpec(r, spec))
		}
		if err != nil {
			return ErrInvalid
		}
	}
	for _, arg := range r.Arguments {
		if strings.ContainsRune(arg, 0) {
			return ErrInvalid
		}
	}
	if err := m.Store.WithWorkspace(r.Key, creating, nil); err != nil {
		return err
	}
	rows, err := m.Store.Snapshot(r.Key)
	if errors.Is(err, bench.ErrNotFound) && creating {
		return nil
	}
	if err != nil {
		return err
	}
	b := &rows[0]
	if b.Workspace.Transfer != nil && r.Operation != "archive" && r.Operation != "inspect-archive" && r.Operation != "reap" || creating && b.Workspace.TransferredFrom != nil {
		return ErrConflict
	}
	if b.Workspace.InitializationRepair != nil && (creating || b.Workspace.Phase != "ready" && r.Operation != "stop" && r.Operation != "stop_admin") {
		return ErrConflict
	}
	if growth && (!utf8.ValidString(b.Path) || len(b.Path) > 4096) {
		return ErrInvalid
	}
	if r.RepositoryName != "" {
		found := false
		for _, member := range b.Workspace.Members {
			found = found || member.Name == r.RepositoryName
		}
		if !found {
			return ErrInvalid
		}
	}
	if r.LeaseToken != "" {
		return m.Store.CheckWorkspaceOwner(r.Key, r.LeaseToken, r.Holder)
	}
	if r.Operation == "lease" || r.Operation == "run" || r.Operation == "setup" {
		if b.Lease != nil {
			return bench.ErrBusy
		}
		if b.Workspace.Phase != "ready" || b.Workspace.Pending != nil {
			return ErrConflict
		}
	}
	return nil
}

// CreationID binds retries to the authenticated peer and canonical request.
func CreationID(r protocolv2.Request, uid uint32) (string, error) {
	digest, err := r.Digest()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Version                      string
		UID                          uint32
		Operation, RequestID, Digest string
	}{r.Version, uid, r.Operation, r.RequestID, digest})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func result(b *model.Bench, token string, root bool) *Result {
	if b == nil {
		return nil
	}
	b = workspace.Clone(b)
	r := &Result{Workspace: b, RootPath: root}
	if b.Lease != nil {
		if token != "" && subtle.ConstantTimeCompare([]byte(b.Lease.Token), []byte(token)) == 1 {
			r.GrantToken = token
		}
		b.Lease.Token, b.Lease.Continuation = "", ""
	}
	for _, member := range b.Workspace.Members {
		status := "pending"
		for _, p := range b.Workspace.Preparation {
			if p.Name == member.Name {
				status = "prepared"
			}
		}
		r.Preparation = append(r.Preparation, Preparation{Name: member.Name, Status: status})
	}
	return r
}

// Execute expects transport validation and authentication before dispatch. It
// repeats read-only preflight because callers other than HTTP also use it.
func (m Manager) Execute(ctx context.Context, r protocolv2.Request, uid uint32) (*Result, error) {
	if err := m.Preflight(r); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch r.Operation {
	case "create", "recover":
		return m.create(ctx, r, uid)
	case "lease", "run", "setup":
		return m.work(ctx, r)
	case "checkpoint", "release", "stop":
		if err := m.Store.CheckWorkspaceOwner(r.Key, r.LeaseToken, ""); err != nil {
			return nil, err
		}
		var b *model.Bench
		var err error
		switch r.Operation {
		case "checkpoint":
			b, err = m.Store.CheckpointWorkspaceContext(ctx, r.Key, r.LeaseToken)
		case "release":
			b, err = m.Store.ReleaseWorkspaceContext(ctx, r.Key, r.LeaseToken)
		case "stop":
			b, err = m.Store.StopWorkspace(r.Key, r.LeaseToken, false)
		}
		return result(b, "", false), err
	case "stop_admin":
		b, err := m.Store.StopWorkspace(r.Key, "", true)
		if err != nil {
			return nil, err
		}
		if b == nil || b.Lease == nil || !b.Lease.StopRequested || b.Lease.Continuation != "" || b.Lease.Execution != nil && b.Lease.Execution.Phase != "stopped" {
			return nil, ErrUnavailable
		}
		if !utf8.ValidString(b.Path) || len(b.Path) > 4096 {
			return result(b, "", false), nil
		}
		return result(b, b.Lease.Token, true), nil
	case "archive":
		b, err := m.Store.ArchiveWorkspaceContext(ctx, r.Key, archivepolicy.DefaultGrace, nil)
		return result(b, "", false), err
	}
	return nil, ErrInvalid
}
