package quotahelper

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/quotaoperation"
)

const MaxReply = 65536

var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Info struct{ Version, Root, Authority, BinarySHA256 string }
type ApplyReply struct {
	Version    string
	Identity   quotaoperation.Identity
	Assignment Assignment
}

func strictReply(raw []byte, v any) error {
	if len(raw) > MaxReply || !utf8.Valid(raw) || json.Unmarshal(raw, v) != nil {
		return errors.New("invalid fixed-helper reply")
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return errors.New("invalid fixed-helper reply")
	}
	canonical, err := json.Marshal(v)
	if err != nil || !bytes.Equal(compact.Bytes(), canonical) {
		return errors.New("ambiguous fixed-helper reply")
	}
	return nil
}
func validInfo(i Info) bool {
	return i.Version == quotaoperation.Version && digest.MatchString(i.Authority) && digest.MatchString(i.BinarySHA256) && filepath.IsAbs(i.Root) && filepath.Clean(i.Root) == i.Root && len(i.Root) <= 4096 && strings.IndexByte(i.Root, 0) < 0
}
