// Package payloadexec owns the managed payload identity boundary. It is separate
// from daemon Git supervision and is not enabled without its fixed host policy.
package payloadexec

import (
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

// Compatibility names preserve the existing helper policy API and error identity.
var ErrPolicy = payloadpolicy.ErrPolicy

type Policy = payloadpolicy.Policy

const LayoutVersion = payloadpolicy.LayoutVersion

func directory(path string) bool { return payloadpolicy.Directory(path) }

// Environment never inherits daemon credentials, loader hooks, shell startup
// programs, sudo metadata or daemon Git configuration. HOME and TMPDIR must come
// from the validated private payload layout, not the ambient environment.
func Environment(home, tmp string, ambient []string) ([]string, error) {
	if !directory(home) || !directory(tmp) || home == tmp {
		return nil, ErrPolicy
	}
	result := []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + tmp, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	ambientEnvironment, err := AmbientEnvironment(ambient)
	if err != nil {
		return nil, err
	}
	return append(result, ambientEnvironment...), nil
}

// AmbientEnvironment returns only the existing bounded locale/CI allowlist. It
// conveys no HOME/TMPDIR, Git settings, loader metadata, credentials or PATH.
// The fixed launcher and final payload each construct their own trusted prefix.
func AmbientEnvironment(ambient []string) ([]string, error) {
	var result []string
	allowed := map[string]bool{"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true, "TERM": true, "CI": true}
	seen := map[string]bool{}
	for _, item := range ambient {
		key, value, ok := strings.Cut(item, "=")
		if !allowed[key] {
			continue
		}
		if !ok || seen[key] || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, ErrPolicy
		}
		seen[key] = true
		result = append(result, item)
	}
	return result, nil
}
