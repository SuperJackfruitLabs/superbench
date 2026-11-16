package finiteio

import (
	"bytes"
	"encoding/json"
	"io"
)

func objectFields(raw []byte, allowed ...string) (map[string]json.RawMessage, error) {
	if len(raw) > MaxBody {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrInvalid
		}
		known := false
		for _, name := range allowed {
			known = known || key == name
		}
		if !known {
			return nil, ErrInvalid
		}
		if _, ok := fields[key]; ok {
			return nil, ErrInvalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(value, []byte("null")) {
			return nil, ErrInvalid
		}
		fields[key] = value
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') || len(fields) != len(allowed) {
		return nil, ErrInvalid
	}
	var extra json.RawMessage
	if d.Decode(&extra) != io.EOF {
		return nil, ErrInvalid
	}
	return fields, nil
}
func (i *Input) UnmarshalJSON(raw []byte) error {
	if i == nil {
		return ErrInvalid
	}
	fields, err := objectFields(raw, "version", "stdin_base64", "stdout_limit_bytes")
	if err != nil {
		return err
	}
	var next Input
	if json.Unmarshal(fields["version"], &next.Version) != nil || json.Unmarshal(fields["stdin_base64"], &next.StdinBase64) != nil || json.Unmarshal(fields["stdout_limit_bytes"], &next.StdoutLimitBytes) != nil {
		return ErrInvalid
	}
	if _, err = next.Bytes(); err != nil {
		return err
	}
	*i = next
	return nil
}
func (o *Output) UnmarshalJSON(raw []byte) error {
	if o == nil {
		return ErrInvalid
	}
	fields, err := objectFields(raw, "version", "stdout_base64", "stdout_bytes", "stdout_sha256")
	if err != nil {
		return err
	}
	var next Output
	if json.Unmarshal(fields["version"], &next.Version) != nil || json.Unmarshal(fields["stdout_base64"], &next.StdoutBase64) != nil || json.Unmarshal(fields["stdout_bytes"], &next.StdoutBytes) != nil || json.Unmarshal(fields["stdout_sha256"], &next.StdoutSHA256) != nil {
		return ErrInvalid
	}
	if err = next.Validate(Input{Version: Version, StdoutLimitBytes: MaxBytes}); err != nil {
		return err
	}
	*o = next
	return nil
}
