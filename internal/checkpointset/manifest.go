// Package checkpointset validates portable immutable multi-repository checkpoints.
package checkpointset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/model"
)

const Version = "superbench.checkpoint.v1"
const MaxRaw = 256 << 10

var logicalName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var identifier = regexp.MustCompile(`^[0-9a-f]{32}$`)
var sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var scpUsername = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var scpHostname = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

var rootFields = map[string]bool{"version": true, "bench_id": true, "generation_id": true, "checkpoint_repository": true, "members": true, "previous_manifest_commit": true}
var memberFields = map[string]bool{"name": true, "repository": true, "task_ref": true, "pin_ref": true, "object_format": true, "commit": true}

type Manifest struct {
	Version                string   `json:"version"`
	BenchID                string   `json:"bench_id"`
	GenerationID           string   `json:"generation_id"`
	CheckpointRepository   string   `json:"checkpoint_repository"`
	Members                []Member `json:"members"`
	PreviousManifestCommit *string  `json:"previous_manifest_commit,omitempty"`
}

type Member struct {
	Name         string `json:"name"`
	Repository   string `json:"repository"`
	TaskRef      string `json:"task_ref"`
	PinRef       string `json:"pin_ref"`
	ObjectFormat string `json:"object_format"`
	Commit       string `json:"commit"`
}

// Parse rejects ambiguous JSON before decoding the exact versioned payload.
// Errors never contain repository identities or other supplied manifest text.
func Parse(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > MaxRaw || !utf8.Valid(raw) || !scalarEscapes(raw) {
		return m, errors.New("checkpoint manifest exceeds bounds or is not UTF-8")
	}
	scanner := json.NewDecoder(bytes.NewReader(raw))
	scanner.UseNumber()
	if err := scanValue(scanner, 0); err != nil {
		return m, errors.New("invalid checkpoint manifest JSON")
	}
	if _, err := scanner.Token(); err != io.EOF {
		return m, errors.New("invalid checkpoint manifest JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return Manifest{}, errors.New("invalid checkpoint manifest fields")
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Digest binds external evidence to the exact bytes, including whitespace and
// key order. It is deliberately absent from the manifest payload itself.
func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (m Manifest) Validate() error {
	if !validStrings(m.Version, m.BenchID, m.GenerationID, m.CheckpointRepository) {
		return errors.New("checkpoint manifest strings must contain Unicode scalars")
	}
	if m.Version != Version || !identifier.MatchString(m.BenchID) || !identifier.MatchString(m.GenerationID) || !logicalName.MatchString(m.CheckpointRepository) || len(m.Members) < 1 || len(m.Members) > 32 {
		return errors.New("invalid checkpoint manifest identity or member count")
	}
	coordinatorFormat := ""
	identities := map[[2]string]bool{}
	for i, member := range m.Members {
		if !validStrings(member.Name, member.Repository, member.TaskRef, member.PinRef, member.ObjectFormat, member.Commit) {
			return errors.New("checkpoint member strings must contain Unicode scalars")
		}
		expected, _ := PinRef(m.BenchID, m.GenerationID, member.Name)
		if !logicalName.MatchString(member.Name) || i > 0 && m.Members[i-1].Name >= member.Name || !validRepository(member.Repository) || !utf8.ValidString(member.TaskRef) || !model.ValidBranchRef(member.TaskRef) || member.PinRef != expected {
			return errors.New("invalid checkpoint member identity, ordering or refs")
		}
		if !validCommit(member.ObjectFormat, member.Commit) {
			return errors.New("invalid checkpoint member object format or commit")
		}
		identity := [2]string{member.Repository, member.TaskRef}
		if identities[identity] {
			return errors.New("duplicate checkpoint repository and task ref")
		}
		identities[identity] = true
		if member.Name == m.CheckpointRepository {
			coordinatorFormat = member.ObjectFormat
		}
	}
	if coordinatorFormat == "" {
		return errors.New("checkpoint repository is not a member")
	}
	if m.PreviousManifestCommit != nil && !validCommit(coordinatorFormat, *m.PreviousManifestCommit) {
		return errors.New("invalid previous manifest commit for checkpoint repository object format")
	}
	return nil
}

func MetadataRef(benchID string) (string, error) {
	if !identifier.MatchString(benchID) {
		return "", errors.New("invalid checkpoint bench ID")
	}
	return "refs/heads/superbench/checkpoints/" + benchID, nil
}

func PinRef(benchID, generationID, name string) (string, error) {
	if !identifier.MatchString(benchID) || !identifier.MatchString(generationID) || !logicalName.MatchString(name) {
		return "", errors.New("invalid checkpoint pin identity")
	}
	return "refs/tags/superbench/pins/" + benchID + "/" + generationID + "/" + name, nil
}

func validCommit(format, commit string) bool {
	return format == "sha1" && sha1Pattern.MatchString(commit) || format == "sha256" && sha256Pattern.MatchString(commit)
}

func validRepository(repository string) bool {
	if len(repository) < 1 || len(repository) > 4096 || !utf8.ValidString(repository) || repository[0] == '-' || strings.ContainsAny(repository, "\x00\r\n") {
		return false
	}
	// Absolute filesystem paths retain literal punctuation and spaces as identity.
	if strings.HasPrefix(repository, "/") {
		return true
	}
	if strings.Contains(repository, "://") {
		parsed, err := url.Parse(repository)
		if err != nil || parsed.Opaque != "" || strings.ContainsAny(repository, "?#") {
			return false
		}
		if strings.HasPrefix(parsed.Host, "[") &&
			(!strings.Contains(parsed.Hostname(), ":") || net.ParseIP(parsed.Hostname()) == nil) {
			return false
		}
		if port := parsed.Port(); port != "" {
			if _, err := strconv.ParseUint(port, 10, 16); err != nil {
				return false
			}
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
			return parsed.User == nil && parsed.Hostname() != ""
		case "ssh":
			if parsed.Hostname() == "" {
				return false
			}
			if parsed.User != nil {
				if _, password := parsed.User.Password(); password || parsed.User.Username() == "" {
					return false
				}
			}
			return true
		case "file":
			localhost := strings.EqualFold(parsed.Host, "localhost")
			return parsed.User == nil && (parsed.Host == "" || localhost) &&
				(strings.HasPrefix(parsed.Path, "/") || parsed.Path == "")
		default:
			return false
		}
	}
	// SCP syntax permits username identity but no password. A slash before the
	// colon would instead be a relative local path, which is not a stable identity.
	user, address, ok := strings.Cut(repository, "@")
	if !ok || !scpUsername.MatchString(user) {
		return false
	}
	if strings.HasPrefix(address, "[") {
		end := strings.Index(address, "]:")
		if end < 0 || len(address[end+2:]) == 0 {
			return false
		}
		host := address[1:end]
		return strings.Contains(host, ":") && net.ParseIP(host) != nil
	}
	host, path, ok := strings.Cut(address, ":")
	if !ok || !scpHostname.MatchString(host) || path == "" {
		return false
	}
	return true
}

func validStrings(values ...string) bool {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

// encoding/json replaces lone UTF-16 escapes with U+FFFD. Reject that lossy
// conversion while retaining literal U+FFFD and valid paired astral characters.
func scalarEscapes(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			if i+1 >= len(raw) {
				return false
			}
			if raw[i+1] != 'u' {
				i++
				continue
			}
			if i+6 > len(raw) {
				return false
			}
			code, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
			if err != nil {
				return false
			}
			if code >= 0xdc00 && code <= 0xdfff {
				return false
			}
			if code >= 0xd800 && code <= 0xdbff {
				if i+12 > len(raw) || string(raw[i+6:i+8]) != `\u` {
					return false
				}
				low, err := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
			i += 5
		}
	}
	return true
}

// scanValue catches duplicate keys at every depth; encoding/json otherwise
// keeps the last occurrence. Only object/array/string values exist in v1.
func scanValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("excessive JSON nesting")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case string:
		return nil
	case json.Delim:
		switch value {
		case '{':
			seen := map[string]bool{}
			allowed := memberFields
			if depth == 0 {
				allowed = rootFields
			}
			for decoder.More() {
				token, err = decoder.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok || seen[key] || !allowed[key] {
					return errors.New("duplicate or invalid JSON key")
				}
				seen[key] = true
				if err = scanValue(decoder, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err = scanValue(decoder, depth+1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if value == '{' && end != json.Delim('}') || value == '[' && end != json.Delim(']') {
			return errors.New("invalid JSON delimiter")
		}
		return nil
	default:
		return errors.New("invalid JSON value type")
	}
}
