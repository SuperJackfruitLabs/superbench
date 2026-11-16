// Package finiteio describes bounded opaque execution bytes. These values carry
// no lifecycle, scope, registration, ownership or execution authority.
package finiteio

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

const Version = "superbench.finite-io.v1"
const MaxBytes = 1 << 20
const MaxBody = 2 << 20
const MaxLookup = 16 << 20

var ErrInvalid = errors.New("invalid finite execution IO")

type Input struct {
	Version          string `json:"version"`
	StdinBase64      string `json:"stdin_base64"`
	StdoutLimitBytes uint64 `json:"stdout_limit_bytes"`
}

type Output struct {
	Version      string `json:"version"`
	StdoutBase64 string `json:"stdout_base64"`
	StdoutBytes  uint64 `json:"stdout_bytes"`
	StdoutSHA256 string `json:"stdout_sha256"`
}

func decode(raw string) ([]byte, error) {
	if len(raw) > base64.StdEncoding.EncodedLen(MaxBytes) {
		return nil, ErrInvalid
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || len(decoded) > MaxBytes || base64.StdEncoding.EncodeToString(decoded) != raw {
		return nil, ErrInvalid
	}
	return decoded, nil
}
func (i Input) Bytes() ([]byte, error) {
	if i.Version != Version || i.StdoutLimitBytes < 1 || i.StdoutLimitBytes > MaxBytes {
		return nil, ErrInvalid
	}
	return decode(i.StdinBase64)
}

// Observe computes byte spelling and hashes only. The caller must separately
// establish actual fencing, complete drain and seal before publishing a result.
func Observe(data []byte, input Input) (Output, error) {
	if _, err := input.Bytes(); err != nil || len(data) > MaxBytes || uint64(len(data)) > input.StdoutLimitBytes {
		return Output{}, ErrInvalid
	}
	sum := sha256.Sum256(data)
	return Output{Version: Version, StdoutBase64: base64.StdEncoding.EncodeToString(data), StdoutBytes: uint64(len(data)), StdoutSHA256: hex.EncodeToString(sum[:])}, nil
}
func (o Output) Validate(input Input) error {
	if _, err := input.Bytes(); err != nil || o.Version != Version {
		return ErrInvalid
	}
	data, err := decode(o.StdoutBase64)
	if err != nil || uint64(len(data)) != o.StdoutBytes || o.StdoutBytes > input.StdoutLimitBytes {
		return ErrInvalid
	}
	sum := sha256.Sum256(data)
	if o.StdoutSHA256 != hex.EncodeToString(sum[:]) {
		return ErrInvalid
	}
	return nil
}
