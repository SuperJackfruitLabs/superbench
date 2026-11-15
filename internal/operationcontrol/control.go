package operationcontrol

import (
	"context"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Control pins configuration and host identity before planning. It is never a
// claim that a scope started, stopped or fenced. Zero means cooperative/unknown.
type Control struct{ State, Root, HostID, BootID string }

func (c Control) Valid() bool {
	if c == (Control{}) {
		return true
	}
	for _, p := range []string{c.State, c.Root} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || !utf8.ValidString(p) || len(p) > 4096 || strings.IndexByte(p, 0) >= 0 {
			return false
		}
	}
	return hex64.MatchString(c.HostID) && len(c.BootID) > 0 && len(c.BootID) <= 128 && !strings.ContainsAny(c.BootID, "\x00\r\n\t ")
}
func (c Control) Managed() bool { return c != (Control{}) && c.Valid() }

type controlKey struct{}

func WithControl(ctx context.Context, c Control) (context.Context, error) {
	if _, ok := FromContext(ctx); !ok || !c.Managed() {
		return nil, ErrAuthority
	}
	if old, ok := ctx.Value(controlKey{}).(Control); ok {
		if old != c {
			return nil, ErrAuthority
		}
		return ctx, nil
	}
	return context.WithValue(ctx, controlKey{}, c), nil
}
func ControlFromContext(ctx context.Context) Control {
	if ctx == nil {
		return Control{}
	}
	c, _ := ctx.Value(controlKey{}).(Control)
	return c
}
