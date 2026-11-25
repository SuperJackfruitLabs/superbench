// Package remoteproto is the wire contract for benchd's /v2/remote/* socket
// routes: strict request parsing and the reply envelope. It has no authority.
// The contract is proposed; it is pinned in treaty before a hub depends on it.
package remoteproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

const (
	Version = "superbench.remote.v1"
	MaxBody = 16 << 10

	OperationCredential = "writer_certificate"
	OperationStatus     = "remote_status"
	maxPublicKey        = 8 << 10
)

// CertificateAlgorithms are the OpenSSH certificate types a reply's
// certificate line may start with: one for each client key type remoteca
// accepts (ssh-ed25519 and ecdsa-sha2-nistp256). remoteca's tests pin this
// list to what it issues.
var CertificateAlgorithms = []string{"ssh-ed25519-cert-v01@openssh.com", "ecdsa-sha2-nistp256-cert-v01@openssh.com"}

// IsCertificateLine reports whether line starts with one of CertificateAlgorithms
// followed by a space.
func IsCertificateLine(line string) bool {
	algorithm, _, found := strings.Cut(line, " ")
	return found && slices.Contains(CertificateAlgorithms, algorithm)
}

var (
	ErrInvalid  = errors.New("invalid remote request")
	ErrResponse = errors.New("invalid remote response")

	keyPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
	tokenPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// CredentialRequest asks for a writer certificate for PublicKey on the lease
// LeaseToken owns. String never shows the token.
type CredentialRequest struct {
	Version    string `json:"version"`
	Operation  string `json:"operation"`
	Key        string `json:"key"`
	LeaseToken string `json:"lease_token"`
	PublicKey  string `json:"public_key"`
}

func (r CredentialRequest) String() string {
	return "writer_certificate request for " + r.Key
}

// Credential is a signed writer certificate and what a client needs to use it.
type Credential struct {
	Certificate        string `json:"certificate"`
	User               string `json:"user"`
	Address            string `json:"address"`
	HostKey            string `json:"host_key"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	LeaseID            string `json:"lease_id"`
	Generation         uint64 `json:"generation"`
	ValidBefore        string `json:"valid_before"`
	// WorkingDirectory is the absolute path every session starts in.
	WorkingDirectory string `json:"working_directory"`
}

// StatusRequest lists remote executions and audit rows for Key, or for every
// key when Key is empty.
type StatusRequest struct {
	Version   string `json:"version"`
	Operation string `json:"operation"`
	Key       string `json:"key,omitempty"`
}

type RemoteExecution struct {
	Key         string `json:"key"`
	BenchID     string `json:"bench_id"`
	Holder      string `json:"holder"`
	LeaseID     string `json:"lease_id"`
	ExecutionID string `json:"execution_id"`
	Generation  uint64 `json:"generation"`
}

// Certificate is one issued-certificate audit row. Serials are random 63-bit
// values, so they travel as decimal strings.
type Certificate struct {
	Serial      uint64 `json:"serial,string"`
	Key         string `json:"key"`
	BenchID     string `json:"bench_id"`
	LeaseID     string `json:"lease_id"`
	Generation  uint64 `json:"generation"`
	ExecutionID string `json:"execution_id"`
	Holder      string `json:"holder"`
	Fingerprint string `json:"fingerprint"`
	IssuedAt    string `json:"issued_at"`
	ValidBefore string `json:"valid_before"`
}

// Session is one session audit row; EndedAt and EndReason are empty while open.
type Session struct {
	ID          int64  `json:"id"`
	Serial      uint64 `json:"serial,string"`
	Key         string `json:"key"`
	BenchID     string `json:"bench_id"`
	Fingerprint string `json:"fingerprint"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	CommandName string `json:"command_name"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at"`
	BytesIn     uint64 `json:"bytes_in"`
	BytesOut    uint64 `json:"bytes_out"`
	EndReason   string `json:"end_reason"`
}

type Status struct {
	Address            string            `json:"address"`
	HostKeyFingerprint string            `json:"host_key_fingerprint"`
	Remote             []RemoteExecution `json:"remote"`
	Certificates       []Certificate     `json:"certificates"`
	Sessions           []Session         `json:"sessions"`
}

// APIError is a refused request. Messages are static and never echo a token.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

type Response struct {
	Version   string          `json:"version"`
	Operation string          `json:"operation"`
	OK        bool            `json:"ok"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *APIError       `json:"error,omitempty"`
}

// fields reads one flat JSON object of string values: no duplicate, unknown
// or missing name, nothing after it, at most MaxBody bytes.
func fields(raw []byte, required, optional []string) (map[string]string, bool) {
	if len(raw) > MaxBody {
		return nil, false
	}
	allowed := map[string]bool{}
	for _, name := range append(append([]string(nil), required...), optional...) {
		allowed[name] = true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	out := map[string]string{}
	for d.More() {
		t, err := d.Token()
		name, ok := t.(string)
		if _, seen := out[name]; err != nil || !ok || seen || !allowed[name] {
			return nil, false
		}
		t, err = d.Token()
		value, ok := t.(string)
		if err != nil || !ok {
			return nil, false
		}
		out[name] = value
	}
	if t, err := d.Token(); err != nil || t != json.Delim('}') {
		return nil, false
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, false
	}
	for _, name := range required {
		if _, ok := out[name]; !ok {
			return nil, false
		}
	}
	return out, true
}

func printable(s string) bool {
	return s != "" && len(s) <= maxPublicKey && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// ParseCredentialRequest accepts exactly the five string fields, the version,
// the writer_certificate operation, a workspace key and a 32-hex lease token.
// The public key is only bounded here; remoteca decides whether it is accepted.
func ParseCredentialRequest(raw []byte) (CredentialRequest, error) {
	f, ok := fields(raw, []string{"version", "operation", "key", "lease_token", "public_key"}, nil)
	if !ok || f["version"] != Version || f["operation"] != OperationCredential || !keyPattern.MatchString(f["key"]) ||
		!tokenPattern.MatchString(f["lease_token"]) || !printable(f["public_key"]) {
		return CredentialRequest{}, ErrInvalid
	}
	return CredentialRequest{Version: Version, Operation: OperationCredential, Key: f["key"], LeaseToken: f["lease_token"], PublicKey: f["public_key"]}, nil
}

// ParseStatusRequest accepts the version, the remote_status operation and an
// optional workspace key.
func ParseStatusRequest(raw []byte) (StatusRequest, error) {
	f, ok := fields(raw, []string{"version", "operation"}, []string{"key"})
	key, keyed := f["key"]
	if !ok || f["version"] != Version || f["operation"] != OperationStatus || keyed && !keyPattern.MatchString(key) {
		return StatusRequest{}, ErrInvalid
	}
	return StatusRequest{Version: Version, Operation: OperationStatus, Key: key}, nil
}

// ParseResponse checks a reply envelope against its HTTP status and operation.
// It returns the result of a 200 reply, or the reply's *APIError.
func ParseResponse(raw []byte, status int, operation string) (json.RawMessage, error) {
	var r Response
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.More() || r.Version != Version || r.Operation != operation {
		return nil, ErrResponse
	}
	if r.OK != (status == http.StatusOK) {
		return nil, ErrResponse
	}
	if r.OK {
		if r.Error != nil || len(r.Result) == 0 || string(r.Result) == "null" {
			return nil, ErrResponse
		}
		return r.Result, nil
	}
	if r.Result != nil || r.Error == nil || r.Error.Code == "" || r.Error.Message == "" {
		return nil, ErrResponse
	}
	return nil, r.Error
}

// Decode reads a result strictly into v.
func Decode(result json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(result))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.More() {
		return ErrResponse
	}
	return nil
}
