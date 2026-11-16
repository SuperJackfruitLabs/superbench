package protocolv2

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// RegistrationObserveRequest is a separate PROPOSED private operator endpoint.
// It cannot select a physical path, process scope, payload UID or execution.
type RegistrationObserveRequest struct {
	Version    string `json:"version"`
	Operation  string `json:"operation"`
	Key        string `json:"key"`
	BenchID    string `json:"bench_id"`
	Holder     string `json:"holder"`
	LeaseToken string `json:"lease_token"`
}
type RegistrationNode struct {
	Device string `json:"device"`
	Inode  string `json:"inode"`
}
type RegistrationMember struct {
	Name     string           `json:"name"`
	Checkout RegistrationNode `json:"checkout"`
	Admin    RegistrationNode `json:"admin"`
	Home     RegistrationNode `json:"home"`
	Temp     RegistrationNode `json:"tmp"`
}
type RegistrationCheckpoint struct {
	GenerationID   string   `json:"generation_id"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	Members        []Commit `json:"members"`
	Seal           string   `json:"seal"`
}
type RegistrationObservation struct {
	Key           string                 `json:"key"`
	BenchID       string                 `json:"bench_id"`
	RootPath      string                 `json:"root_path"`
	LayoutVersion string                 `json:"layout_version"`
	PolicySHA256  string                 `json:"policy_sha256"`
	Root          RegistrationNode       `json:"root"`
	RootHome      RegistrationNode       `json:"root_home"`
	RootTemp      RegistrationNode       `json:"root_tmp"`
	Members       []RegistrationMember   `json:"members"`
	Checkpoint    RegistrationCheckpoint `json:"checkpoint"`
}
type RegistrationObserveResponse struct {
	Version   string                   `json:"version"`
	Operation string                   `json:"operation"`
	OK        bool                     `json:"ok"`
	Result    *RegistrationObservation `json:"result,omitempty"`
	Error     *APIError                `json:"error,omitempty"`
}

func validObservationRequest(r RegistrationObserveRequest) bool {
	return r.Version == Version && r.Operation == "registration_observe" && keyPattern.MatchString(r.Key) && idPattern.MatchString(r.BenchID) && holderPattern.MatchString(r.Holder) && tokenPattern.MatchString(r.LeaseToken)
}
func ParseRegistrationObserveRequest(raw []byte) (RegistrationObserveRequest, error) {
	var r RegistrationObserveRequest
	if !cancellationJSON(raw) {
		return r, ErrInvalid
	}
	if _, ok := fieldSet(raw, []string{"version", "operation", "key", "bench_id", "holder", "lease_token"}, nil); !ok {
		return r, ErrInvalid
	}
	if json.Unmarshal(raw, &r) != nil || !validObservationRequest(r) {
		return RegistrationObserveRequest{}, ErrInvalid
	}
	return r, nil
}

// ValidateRegistrationObserveResponse joins shape and complete identity only.
// Schema-valid bytes never establish physical custody, ownership or fencing.
func ValidateRegistrationObserveResponse(response RegistrationObserveResponse, status int, request RegistrationObserveRequest) error {
	raw, err := json.Marshal(response)
	if err != nil {
		return ErrResponse
	}
	_, err = ParseRegistrationObserveResponse(raw, status, request)
	return err
}
func ParseRegistrationObserveResponse(raw []byte, status int, request RegistrationObserveRequest) (RegistrationObserveResponse, error) {
	var zero RegistrationObserveResponse
	if len(raw) > MaxBody || !utf8.Valid(raw) || !scalarEscapes(raw) || !strictJSON(raw) {
		return zero, ErrResponse
	}
	if _, ok := fieldSet(raw, []string{"version", "operation", "ok"}, []string{"result", "error"}); !ok {
		return zero, ErrResponse
	}
	var r RegistrationObserveResponse
	if decodeResponseObject(raw, &r) != nil || r.Version != Version || r.Operation != "registration_observe" {
		return zero, ErrResponse
	}
	if !r.OK {
		if r.Result != nil || r.Error == nil || len(r.Error.Message) == 0 || len(r.Error.Message) > 256 || !validHTTPError(r.Error.Code, status) {
			return zero, ErrResponse
		}
		return r, nil
	}
	if !validObservationRequest(request) || status != 200 || r.Error != nil || r.Result == nil {
		return zero, ErrResponse
	}
	v := r.Result
	if v.Key != request.Key || v.BenchID != request.BenchID || v.LayoutVersion != "superbench.payload-layout.v1" || !digestPattern.MatchString(v.PolicySHA256) || len(v.RootPath) > 4096 || !utf8.ValidString(v.RootPath) || strings.ContainsRune(v.RootPath, 0) || !filepath.IsAbs(v.RootPath) || filepath.Clean(v.RootPath) != v.RootPath || !strings.HasSuffix(v.RootPath, "/benches/"+request.Key) || len(v.Members) < 1 || len(v.Members) > 32 {
		return zero, ErrResponse
	}
	seen := map[RegistrationNode]bool{}
	node := func(n RegistrationNode) bool {
		for _, s := range []string{n.Device, n.Inode} {
			u, e := strconv.ParseUint(s, 10, 64)
			if e != nil || u == 0 || strconv.FormatUint(u, 10) != s {
				return false
			}
		}
		if n.Device != v.Root.Device || seen[n] {
			return false
		}
		seen[n] = true
		return true
	}
	for _, n := range []RegistrationNode{v.Root, v.RootHome, v.RootTemp} {
		if !node(n) {
			return zero, ErrResponse
		}
	}
	cp := v.Checkpoint
	if !idPattern.MatchString(cp.GenerationID) || !digestPattern.MatchString(cp.ManifestSHA256) || !commitPattern.MatchString(cp.Seal) || !validVector(cp.Members, len(v.Members), v.Members[0].Name) {
		return zero, ErrResponse
	}
	for i, m := range v.Members {
		if !namePattern.MatchString(m.Name) || m.Name != cp.Members[i].Name || i > 0 && v.Members[i-1].Name >= m.Name {
			return zero, ErrResponse
		}
		for _, n := range []RegistrationNode{m.Checkout, m.Admin, m.Home, m.Temp} {
			if !node(n) {
				return zero, ErrResponse
			}
		}
	}
	return r, nil
}
