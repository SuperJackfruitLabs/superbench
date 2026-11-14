package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

func Nonce() (string, error) {
	v := make([]byte, 16)
	if _, e := rand.Read(v); e != nil {
		return "", e
	}
	return hex.EncodeToString(v), nil
}

func NewLease(holder string) (*Lease, error) {
	if holder == "" {
		return nil, errors.New("holder required")
	}
	token, e := Nonce()
	if e != nil {
		return nil, e
	}
	return &Lease{Token: token, Holder: holder}, nil
}
