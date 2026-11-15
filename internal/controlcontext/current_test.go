package controlcontext

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestScopedConfigurationRequiresCurrentStateBeforeFallback(t *testing.T) {
	i := operationcontrol.Intent{Target: operationcontrol.Target{RequestID: "create", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("b", 32)}
	ctx, err := operationcontrol.WithScope(context.Background(), i, func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("old state")
	checks, migrations := 0, 0
	_, err = Configure(ctx, Config{Injected: true, Current: func() error { checks++; return failure }, Migrate: func() error { migrations++; return nil }})
	if !errors.Is(err, failure) || checks != 1 || migrations != 0 {
		t.Fatal("scoped fallback bypassed current-state barrier", checks, migrations, err)
	}
}
