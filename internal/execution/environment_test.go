package execution

import (
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
)

func TestCommandCacheEnvironmentOverridesInheritedAliases(t *testing.T) {
	no := false
	paths := cachepolicy.Select("/data", "/task", cachepolicy.Settings{Pnpm: &no, Playwright: &no})
	env := EnvironmentFor(paths, []string{"NPM_CONFIG_CACHE=/outside", "npm_config_cache=/other", "NPM_CONFIG_STORE_DIR=/unbounded", "npm_config_package_import_method=hardlink", "BUN_INSTALL_CACHE_DIR=/old", "PLAYWRIGHT_BROWSERS_PATH=/old", "GOCACHE=/old", "GOMODCACHE=/old", "BENCH_LEASE_TOKEN=private", "PATH=/bin"})
	values := map[string]string{}
	for _, line := range env {
		key, value, _ := strings.Cut(line, "=")
		if _, ok := values[strings.ToLower(key)]; ok {
			t.Fatal("duplicate cache authority", env)
		}
		values[strings.ToLower(key)] = value
	}
	if len(values) != 8 || values["bench_lease_token"] != "" || values["path"] != "/bin" {
		t.Fatal(values)
	}
	if values["npm_config_store_dir"] != "/task/.bench/cache/pnpm" || values["playwright_browsers_path"] != "/task/.bench/cache/playwright" || values["npm_config_cache"] != "/data/store/npm" || values["npm_config_package_import_method"] != "clone" {
		t.Fatal(values)
	}
}

func TestCacheOverridesPreserveUnrelatedCaseSensitiveVariables(t *testing.T) {
	env := Environment("/data", []string{"gocache=custom", "GOCACHE=/outside", "NPM_CONFIG_CACHE=/outside"})
	found := false
	for _, line := range env {
		if line == "gocache=custom" {
			found = true
		}
		if line == "GOCACHE=/outside" || line == "NPM_CONFIG_CACHE=/outside" {
			t.Fatal("recognized override survived", env)
		}
	}
	if !found {
		t.Fatal("unrelated variable was removed", env)
	}
}
