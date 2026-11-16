package payloadcache

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
)

func TestCacheModesPreserveDefaultAndEverySharedLocalSelection(t *testing.T) {
	for n := 0; n < 32; n++ {
		flags := []bool{n&1 != 0, n&2 != 0, n&4 != 0, n&8 != 0, n&16 != 0}
		settings := cachepolicy.Settings{Pnpm: &flags[0], Npm: &flags[1], Bun: &flags[2], Playwright: &flags[3], Go: &flags[4]}
		modes := FromSettings(settings)
		raw, err := modes.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		var parsed Modes
		if err = json.Unmarshal(raw, &parsed); err != nil || parsed != modes {
			t.Fatal("modes changed", n, err)
		}
		paths, err := Paths("/bounded", Selection{Key: "card", Member: "api"}, modes)
		if err != nil {
			t.Fatal(err)
		}
		legacy := cachepolicy.Select("/bounded", "/bounded/benches/card/api", settings)
		want := legacy
		replace := func(p string) string { return strings.Replace(p, "/bounded/store/", "/bounded/payload-store/", 1) }
		want.Pnpm, want.Npm, want.Bun, want.Playwright, want.GoBuild, want.GoMod = replace(want.Pnpm), replace(want.Npm), replace(want.Bun), replace(want.Playwright), replace(want.GoBuild), replace(want.GoMod)
		if !reflect.DeepEqual(paths, want) {
			t.Fatal("shared/local modes changed", n, paths, want)
		}
		if legacy != cachepolicy.Select("/bounded", "/bounded/benches/card/api", settings) {
			t.Fatal("legacy selection mutated")
		}
	}
	defaultModes := FromSettings(cachepolicy.Settings{})
	if !defaultModes.Pnpm || !defaultModes.Npm || !defaultModes.Bun || !defaultModes.Playwright || !defaultModes.Go {
		t.Fatal("nil settings ceased to mean shared")
	}
	raw, err := defaultModes.Bytes()
	if err != nil || string(raw) != `{"version":"superbench.payload-caches.v1","pnpm":true,"npm":true,"bun":true,"playwright":true,"go":true}` {
		t.Fatal("canonical modes changed", string(raw), err)
	}
}

func TestCacheModesRefuseUnknownAliasDuplicateNullTypeAndBounds(t *testing.T) {
	valid := `{"version":"superbench.payload-caches.v1","pnpm":true,"npm":true,"bun":true,"playwright":true,"go":true}`
	for _, raw := range []string{
		"null", strings.TrimSuffix(valid, "}") + strings.Repeat(" ", MaxModes+1) + "}",
		strings.Replace(valid, `"pnpm":true,`, "", 1),
		strings.Replace(valid, `"pnpm":true`, `"Pnpm":true`, 1),
		strings.Replace(valid, `"pnpm":true`, `"pnpm":true,"pnpm":true`, 1),
		strings.Replace(valid, `"pnpm":true`, `"pnpm":null`, 1),
		strings.Replace(valid, `"pnpm":true`, `"pnpm":1`, 1),
		strings.Replace(valid, `"pnpm":true`, `"pnpm":"shared"`, 1),
		strings.TrimSuffix(valid, "}") + `,"path":"/arbitrary"}`,
		strings.Replace(valid, Version, "future", 1),
	} {
		var m Modes
		if err := json.Unmarshal([]byte(raw), &m); !errors.Is(err, ErrModes) {
			t.Fatal("cache modes did not refuse precisely", raw, err)
		}
	}
}

func TestCacheEnvironmentDerivesOnlyFixedOverridesWithoutCreatingPaths(t *testing.T) {
	local := false
	m := FromSettings(cachepolicy.Settings{Go: &local})
	env, err := Environment("/bounded", Selection{Key: "card", Scalar: true}, m)
	want := []string{"npm_config_cache=/bounded/payload-store/npm", "npm_config_store_dir=/bounded/payload-store/pnpm", "npm_config_package_import_method=clone", "BUN_INSTALL_CACHE_DIR=/bounded/payload-store/bun", "PLAYWRIGHT_BROWSERS_PATH=/bounded/payload-store/playwright", "GOCACHE=/bounded/benches/card/worktree/.bench/cache/go-build", "GOMODCACHE=/bounded/benches/card/worktree/.bench/cache/go-mod"}
	if err != nil || !reflect.DeepEqual(env, want) {
		t.Fatal("fixed environment changed", env, err)
	}
	for _, s := range []Selection{{Key: "../card", Member: "api"}, {Key: "card"}, {Key: "card", Root: true, Member: "api"}, {Key: "card", Scalar: true, Member: "api"}, {Key: "card", Member: "../api"}} {
		if _, err := Paths("/bounded", s, m); !errors.Is(err, ErrModes) {
			t.Fatal("ambiguous selection accepted", s, err)
		}
	}
	if _, err := Paths("/bounded", Selection{Key: "card", Root: true}, m); !errors.Is(err, ErrModes) {
		t.Fatal("aggregate root silently inherited member-local settings", err)
	}
	if legacy := cachepolicy.Select("/bounded", "", cachepolicy.Settings{}); legacy.Pnpm != "/bounded/store/pnpm" {
		t.Fatal("legacy cache root changed", legacy)
	}
}
