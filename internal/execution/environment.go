package execution

import (
	"path/filepath"
	"strings"
)

func Environment(data string, env []string) []string {
	overrides := map[string]string{
		"npm_config_cache":         filepath.Join(data, "store", "npm"),
		"BUN_INSTALL_CACHE_DIR":    filepath.Join(data, "store", "bun"),
		"PLAYWRIGHT_BROWSERS_PATH": filepath.Join(data, "store", "playwright"),
		"GOCACHE":                  filepath.Join(data, "store", "go-build"),
		"GOMODCACHE":               filepath.Join(data, "store", "go-mod"),
	}
	clean := commandEnvironment(env)
	var result []string
	for _, v := range clean {
		key, _, _ := strings.Cut(v, "=")
		if _, replace := overrides[key]; !replace {
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
