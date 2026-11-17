//go:build linux

// Package payloadgit is the explicit registered-checkout Git route. It never
// discovers a registry, converts a layout or falls back to daemon-owned Git.
package payloadgit

import (
	"bytes"
	"context"
	"errors"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitpolicy"
	"io"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/gitcontrol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadexec"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadlaunch"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

var ErrRefused = errors.New("registered payload Git refused")

// Route is bound to an installed service identity and one exact aggregate member
// vector supplied under the caller's existing held owner/initialization lock.
// The zero value refuses. It grants neither a lease nor operation admission.
type Route struct {
	authority       serviceidentity.Authority
	key, id, member string
	directory       string
	members         []model.WorkspaceMember
	scalar          bool
	input           payloadlaunch.GitInput
	layout          *payloadexec.Layout
	observe         bool
}

// observations are the only argv an owner-free Observe route executes: fixed
// read-only identity and cleanliness queries, never a ref, index or file write.
var observations = map[string]bool{}

func init() {
	for _, args := range [][]string{
		{"rev-parse", "--show-toplevel"},
		{"rev-parse", "--path-format=absolute", "--git-common-dir"},
		{"rev-parse", "--show-object-format=storage"},
		{"rev-parse", "--verify", "HEAD^{commit}"},
		{"symbolic-ref", "--quiet", "HEAD"},
		{"config", "--null", "--get-all", "remote.origin.url"},
		{"status", "--porcelain", "--untracked-files=all"},
	} {
		observations[strings.Join(args, "\x00")] = true
	}
}

// New requires a held owner and durable allocation identity. The caller keeps
// its store/owner lock across Execute, including its actual scope fence.
func New(authority serviceidentity.Authority, b *model.Bench, member string) (Route, error) {
	p, err := authority.Policy()
	if err != nil || b == nil || b.Lease == nil || b.Lease.Token == "" || b.Lease.Holder == "" || b.Lease.StopRequested || b.ArchivedAt != "" {
		return Route{}, ErrRefused
	}
	return newRoute(authority, p.DataRoot, b, member)
}

// Observe binds an owner-free route to one registered aggregate member. It
// grants no lease or admission: Execute then accepts only fixed observations.
func Observe(authority serviceidentity.Authority, b *model.Bench, member string) (Route, error) {
	p, err := authority.Policy()
	if err != nil || b == nil || b.Workspace == nil || b.ArchivedAt != "" {
		return Route{}, ErrRefused
	}
	r, err := newRoute(authority, p.DataRoot, b, member)
	r.observe = err == nil
	return r, err
}

func newRoute(authority serviceidentity.Authority, data string, b *model.Bench, member string) (Route, error) {
	expectedRoot := filepath.Join(data, "benches", b.Key)
	if b.Workspace == nil {
		if member != "" || b.RegistrationID == "" || b.PayloadLayoutVersion == "" || model.ValidateScalarLayout(b) != nil || b.AllocationRoot != expectedRoot {
			return Route{}, ErrRefused
		}
		return Route{authority: authority, key: b.Key, id: b.RegistrationID, scalar: true, directory: b.Path}, nil
	}
	if b.Path != expectedRoot {
		return Route{}, ErrRefused
	}
	found := false
	last := ""
	for _, m := range b.Workspace.Members {
		if m.Name == "" || m.Name <= last {
			return Route{}, ErrRefused
		}
		last = m.Name
		found = found || m.Name == member
		if m.Path != filepath.Join(b.Path, m.Name) {
			return Route{}, ErrRefused
		}
	}
	if !found {
		return Route{}, ErrRefused
	}
	return Route{authority: authority, key: b.Key, id: b.Workspace.ID, member: member, directory: filepath.Join(b.Path, member), members: append([]model.WorkspaceMember(nil), b.Workspace.Members...)}, nil
}

// Execute accepts finite owned input and existing approved bounds only. Output
// remains private until the runner has fenced and drained the exact Git scope.
// Caller-specific author/config/credential environment is deliberately absent;
// such commands must refuse at their adapter until explicitly supported.
func (r Route) Execute(ctx context.Context, runner gitcontrol.Runner, args []string, input []byte, inputLimit, outputLimit uint64) ([]byte, error) {
	if !gitpolicy.Subcommand(args) || r.observe && !observations[strings.Join(args, "\x00")] {
		return nil, errors.Join(ErrRefused, gitpolicy.ErrPolicy)
	}
	if outputLimit == 0 || uint64(len(input)) > inputLimit {
		return nil, ErrRefused
	}
	p, err := r.authority.Policy()
	if err != nil || runner.State != p.StateRoot || runner.Root != p.CgroupRoot {
		return nil, ErrRefused
	}
	r.input = payloadlaunch.GitInput{Bytes: bytes.Clone(input), Limit: inputLimit}
	out := &bounded{limit: outputLimit}
	stderr := &bounded{limit: min(outputLimit, 64<<10)} // diagnostics stay small
	cmd := gitcontrol.ManagedCommand(ctx, r.directory, args...)
	cmd.Env = []string{}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err = runner.RunPayload(ctx, cmd, &r); err != nil {
		return nil, errors.Join(ErrRefused, err)
	}
	return bytes.Clone(out.Bytes()), nil
}

func (r *Route) Prepare(cmd *exec.Cmd, scope *execution.Execution) (gitcontrol.Transport, error) {
	if r == nil || scope == nil {
		return nil, ErrRefused
	}
	p, err := r.authority.Policy()
	if err != nil {
		return nil, ErrRefused
	}
	request := payloadexec.Request{Version: payloadexec.Version, Key: r.key, LayoutID: r.id, Member: r.member, Kind: "git", Scope: payloadexec.Scope{ID: scope.ID, HostID: scope.HostID, BootID: scope.BootID}, Arguments: append([]string(nil), cmd.Args...)}
	registry, err := serviceidentity.ForPolicy(p)
	if err != nil {
		return nil, ErrRefused
	}
	request.PolicyDigest = registry.PolicySHA256
	layout, err := payloadexec.LoadLayout(p, request)
	if err != nil || !r.matchesLayout(layout) {
		return nil, ErrRefused
	}
	r.layout = &layout
	bridge, err := payloadlaunch.Prepare(cmd, payloadlaunch.Target{Key: r.key, LayoutID: r.id, Member: r.member, PolicyDigest: request.PolicyDigest, Kind: "git"}, scope, r.input)
	if err != nil {
		return nil, ErrRefused
	}
	return &transport{Bridge: bridge, route: r, request: request, guard: payloadlaunch.GuardCommand(cmd)}, nil
}

// Scalar routes have no grouped member vector. Both forms must match the
// registered shape before any indexed member comparison or payload launch.
func (r Route) matchesLayout(l payloadexec.Layout) bool {
	if l.Scalar != r.scalar {
		return false
	}
	if r.scalar {
		return r.member == "" && len(r.members) == 0 && len(l.Members) == 1 && l.Members[0].Name == ""
	}
	if len(l.Members) != len(r.members) {
		return false
	}
	for i, m := range l.Members {
		if m.Name != r.members[i].Name {
			return false
		}
	}
	return true
}

type transport struct {
	*payloadlaunch.Bridge
	route   *Route
	request payloadexec.Request
	guard   payloadlaunch.CommandGuard
}

func (t *transport) Check() error {
	if err := t.guard.Check(); err != nil {
		return errors.Join(ErrRefused, err)
	}
	p, err := t.route.authority.Policy()
	if err != nil {
		return ErrRefused
	}
	current, err := payloadexec.LoadLayout(p, t.request)
	if err != nil || t.route.layout == nil || !reflect.DeepEqual(current, *t.route.layout) {
		return ErrRefused
	}
	return nil
}
func (t *transport) Drain(ctx context.Context, f interface {
	Fence(*execution.Execution) error
}) error {
	return t.Bridge.Drain(ctx, f)
}

type bounded struct {
	bytes.Buffer
	limit uint64
}

func (b *bounded) Write(p []byte) (int, error) {
	if uint64(b.Len())+uint64(len(p)) > b.limit {
		return 0, io.ErrShortBuffer
	}
	return b.Buffer.Write(p)
}
