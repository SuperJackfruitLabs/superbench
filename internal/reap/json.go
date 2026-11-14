package reap

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// CheckJSON keeps a reviewed JSON document unambiguous across viewers/decoders.
func CheckJSON(raw []byte) error {
	if len(raw) > MaxManifest || !utf8.Valid(raw) {
		return errors.New("invalid or oversized plan encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("plan nesting exceeds limit")
		}
		token, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("invalid plan field")
				}
				if keys[name] {
					return errors.New("duplicate plan field")
				}
				keys[name] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			closing, e := d.Token()
			if e != nil {
				return e
			}
			if closing != json.Delim('}') {
				return errors.New("invalid plan object")
			}
		case '[':
			for d.More() {
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			closing, e := d.Token()
			if e != nil {
				return e
			}
			if closing != json.Delim(']') {
				return errors.New("invalid plan array")
			}
		default:
			return errors.New("invalid plan document")
		}
		return nil
	}
	if e := value(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing plan data")
	}
	return nil
}
