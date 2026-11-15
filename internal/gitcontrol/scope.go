package gitcontrol

import "context"

// ConfiguredScope exposes the private runner configuration for durable staging
// provenance. It does not establish that a command started, exited or fenced.
// Consumers must still reconcile control intents under the host store lock.
func ConfiguredScope(ctx context.Context) (state, root string, managed bool) {
	r, _ := ctx.Value(runnerContextKey{}).(*Runner)
	if r == nil {
		return "", "", false
	}
	return r.State, r.Root, true
}
