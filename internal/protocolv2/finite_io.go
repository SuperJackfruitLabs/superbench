package protocolv2

import (
	"mime"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
)

const FiniteIOContentType = "application/json; profile=" + finiteio.Version

// BodyLimit classifies the explicit proposed run profile; it grants no feature
// or operation authority. Callers must separately gate the optional crossing.
// Unprofiled JSON keeps its previous parameter handling and body budget.
func BodyLimit(contentType string) (int, error) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || media != "application/json" {
		return 0, ErrInvalid
	}
	profile, present := params["profile"]
	if !present {
		return MaxBody, nil
	}
	// mime.ParseMediaType coalesces identical repeated parameters. This fixed
	// profile value has no semicolons, so exactly one raw separator is required
	// even when the parameter name or quoted value uses an equivalent spelling.
	// Reject RFC2231 extended/continued aliases: the one parameter must literally
	// name profile, rather than be normalized to it by the MIME parser.
	_, parameter, _ := strings.Cut(contentType, ";")
	name, _, equal := strings.Cut(parameter, "=")
	if profile != finiteio.Version || len(params) != 1 || strings.Count(contentType, ";") != 1 || !equal || !strings.EqualFold(strings.TrimSpace(name), "profile") {
		return 0, ErrInvalid
	}
	return finiteio.MaxBody, nil
}

// ParseMedia requires exact profile↔IO agreement. Parse continues to implement
// only the unchanged legacy request shape and 64KiB limit.
func ParseMedia(raw []byte, contentType string) (Request, error) {
	limit, err := BodyLimit(contentType)
	if err != nil {
		return Request{}, err
	}
	return parseRequest(raw, limit, limit == finiteio.MaxBody)
}
