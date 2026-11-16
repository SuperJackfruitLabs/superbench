package finiteio

import (
	"encoding/base64"
	"strconv"
)

const MaxLegacyBody = 64 << 10

// MaxResponseBytes includes the ENTIRE allowed legacy run envelope (workspace,
// checkpoint, ownership, execution and error metadata), not just payload bytes.
// IO adds one final field; canonical base64 never needs JSON string escaping.
// This bound is representability only and confers no runtime or receipt power.
func MaxResponseBytes(input Input) (int, error) {
	if _, err := input.Bytes(); err != nil {
		return 0, err
	}
	fixed := len(`,"io":{"version":"superbench.finite-io.v1","stdout_base64":"","stdout_bytes":,"stdout_sha256":""}`) + 64
	total := MaxLegacyBody + fixed + base64.StdEncoding.EncodedLen(int(input.StdoutLimitBytes)) + len(strconv.FormatUint(input.StdoutLimitBytes, 10))
	if total > MaxBody {
		return 0, ErrInvalid
	}
	return total, nil
}
