package execution

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/cachepolicy"
	"strings"
)

func Environment(data string, env []string) []string {
	return EnvironmentFor(cachepolicy.Select(data, "", cachepolicy.Settings{}), env)
}

func EnvironmentFor(paths cachepolicy.Paths, env []string) []string {
	overrides := map[string]string{
		"npm_config_cache":                 paths.Npm,
		"npm_config_store_dir":             paths.Pnpm,
		"npm_config_package_import_method": "clone",
		"BUN_INSTALL_CACHE_DIR":            paths.Bun,
		"PLAYWRIGHT_BROWSERS_PATH":         paths.Playwright,
		"GOCACHE":                          paths.GoBuild,
		"GOMODCACHE":                       paths.GoMod,
	}
	clean := commandEnvironment(env)
	var result []string
	for _, v := range clean {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(strings.ToLower(key), "npm_config_") {
			key = strings.ToLower(key)
		}
		_, replace := overrides[key]
		if !replace {
			result = append(result, v)
		}
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func commandEnvironment(env []string) []string {
	var clean []string
	for _, v := range env {
		if len(v) >= 18 && v[:18] == "BENCH_LEASE_TOKEN=" {
			continue
		}
		clean = append(clean, v)
	}
	return clean
}
