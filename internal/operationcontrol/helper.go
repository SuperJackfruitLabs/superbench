package operationcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

const MaxHelpers = 3
const MaxHelperIntent = 65536

// Helper binds one fixed logical resource to the actual helper and original
// policy/root authority. State identity is independently captured by the caller;
// the privileged helper never treats these fields as a claimed UID or pathname
// permission. No command arguments, lease tokens or environment are retained.
type Helper struct {
	Identity                quotaoperation.Identity
	Command, Policy, State  string
	StateDevice, StateInode uint64
	Kind, Key               string
	Requested               uint64
}

func (h Helper) StateIdentity() string {
	raw, _ := json.Marshal(struct {
		Domain, Path  string
		Device, Inode uint64
	}{"superbench.helper-state.v1", h.State, h.StateDevice, h.StateInode})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (h Helper) Valid(i Intent) bool {
	if !i.Valid() || h.Identity.OperationID != i.Target.ID() || h.Identity.Epoch != i.Epoch || h.Identity.StateIdentity != h.StateIdentity() || !hex64.MatchString(h.Identity.Authority) || !hex64.MatchString(h.Identity.BinarySHA256) || h.StateDevice == 0 || h.StateInode == 0 {
		return false
	}
	for _, path := range []string{h.Command, h.Policy, h.State} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 || !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 {
			return false
		}
	}
	switch h.Kind {
	case "bench":
		return h.Key == i.Key && (h.Requested == 0 || h.Requested >= 1024 && h.Requested%1024 == 0 && h.Requested <= 1<<50)
	case "shared":
		return (h.Key == "store" || h.Key == "mirrors") && h.Requested == 0
	}
	return false
}

type helperRecorderKey struct{}

func WithHelperRecorder(ctx context.Context, record func(Helper) error) (context.Context, error) {
	if _, ok := FromContext(ctx); !ok || record == nil {
		return nil, ErrAuthority
	}
	if ctx.Value(helperRecorderKey{}) != nil {
		return nil, ErrConflict
	}
	return context.WithValue(ctx, helperRecorderKey{}, record), nil
}

// RecordHelper persists authority before Start. Its owner rechecks durable
// admission while recording; no receipt mutex remains held over helper waits.
func RecordHelper(ctx context.Context, h Helper) error {
	i, ok := FromContext(ctx)
	if !ok || !h.Valid(i) {
		return ErrAuthority
	}
	if err := Check(ctx); err != nil {
		return err
	}
	if c := ControlFromContext(ctx); c.Managed() && h.State != c.State {
		return ErrAuthority
	}
	record, _ := ctx.Value(helperRecorderKey{}).(func(Helper) error)
	if record == nil {
		return ErrAuthority
	}
	return record(h)
}
