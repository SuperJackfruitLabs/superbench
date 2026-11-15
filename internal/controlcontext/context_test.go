package controlcontext

import (
	"context"
	"testing"
)

func TestConfigurationPreservesExistingInjectedAndUnavailableContexts(t *testing.T) {
	type identity struct{}
	existing := context.WithValue(context.Background(), identity{}, "existing")
	for _, kind := range []string{"existing", "injected", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			migrations := 0
			c := Config{Root: t.TempDir(), Migrate: func() error { migrations++; return nil }}
			if kind == "existing" {
				c.Existing = existing
			}
			if kind == "injected" {
				c.Injected = true
			}
			parent := context.Background()
			got, err := Configure(parent, c)
			if err != nil || migrations != 0 {
				t.Fatal("fallback unexpectedly migrated", migrations, err)
			}
			want := parent
			if kind == "existing" {
				want = existing
			}
			if got != want {
				t.Fatal("context changed", got)
			}
		})
	}
}
