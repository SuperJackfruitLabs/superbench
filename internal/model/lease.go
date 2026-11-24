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
	id, e := Nonce()
	if e != nil {
		return nil, e
	}
	return &Lease{ID: id, Generation: 1, Token: token, Holder: holder}, nil
}

// Rotate issues the successor token for the same acquisition: same holder and
// ID, next generation. The caller copies whatever else it keeps.
func (l *Lease) Rotate() (*Lease, error) {
	token, e := Nonce()
	if e != nil {
		return nil, e
	}
	return &Lease{ID: l.ID, Generation: l.Generation + 1, Token: token, Holder: l.Holder}, nil
}
