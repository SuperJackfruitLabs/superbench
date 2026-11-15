// Package hostidentity identifies an observed machine for administrative
// handoff binding. An identity is not a writer fence or an execution capability.
package hostidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

var machine = regexp.MustCompile(`^[0-9a-f]{32}$`)
var platform = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
var property = regexp.MustCompile(`(?m)^\s*"IOPlatformUUID"\s*=\s*"([^"\r\n]*)"\s*$`)

func hash(prefix, value string) string {
	sum := sha256.Sum256([]byte(prefix + value))
	return hex.EncodeToString(sum[:])
}

func linuxIdentity(raw []byte) (string, error) {
	s := strings.TrimSpace(string(raw))
	if len(raw) > 128 || !machine.MatchString(s) || s == strings.Repeat("0", 32) {
		return "", errors.New("a valid stable machine identity is required")
	}
	// Retain the identity domain already used by Linux managed execution.
	return hash("superbench-host:", s), nil
}

func darwinIdentity(raw []byte) (string, error) {
	if len(raw) > 64<<10 {
		return "", errors.New("platform identity observation exceeds its bound")
	}
	matches := property.FindAllSubmatch(raw, -1)
	if len(matches) != 1 || !platform.Match(matches[0][1]) {
		return "", errors.New("one unambiguous platform identity is required")
	}
	s := strings.ToLower(string(matches[0][1]))
	if s == "00000000-0000-0000-0000-000000000000" || s == "ffffffff-ffff-ffff-ffff-ffffffffffff" {
		return "", errors.New("uninitialized platform identity")
	}
	return hash("superbench-host-darwin:", s), nil
}
