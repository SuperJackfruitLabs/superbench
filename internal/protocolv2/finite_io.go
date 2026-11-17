package protocolv2

import (
	"mime"
	"strings"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
)

const FiniteIOContentType = "application/json; profile=" + finiteio.Version

// BodyLimit classifies the explicit proposed run profile; it grants no feature
// or operation authority. Callers must separately gate the optional crossing.
// Unprofiled JSON keeps its previous parameter handling and body budget. The
// finite result is explicit: callers never infer the profile from the limit.
func BodyLimit(contentType string) (limit int, finite bool, err error) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || media != "application/json" {
		return 0, false, ErrInvalid
	}
	profile, present := params["profile"]
	if !present {
		return MaxBody, false, nil
	}
	// mime.ParseMediaType coalesces identical repeated parameters. This fixed
	// profile value has no semicolons, so exactly one raw separator is required
	// even when the parameter name or quoted value uses an equivalent spelling.
	// Reject RFC2231 extended/continued aliases: the one parameter must literally
	// name profile, rather than be normalized to it by the MIME parser.
	_, parameter, _ := strings.Cut(contentType, ";")
	name, _, equal := strings.Cut(parameter, "=")
	if profile != finiteio.Version || len(params) != 1 || strings.Count(contentType, ";") != 1 || !equal || !strings.EqualFold(strings.TrimSpace(name), "profile") {
		return 0, false, ErrInvalid
	}
	return finiteio.MaxBody, true, nil
}

// ParseMedia requires exact profile↔IO agreement. Parse continues to implement
// only the unchanged legacy request shape and 64KiB limit.
func ParseMedia(raw []byte, contentType string) (Request, error) {
	_, finite, err := BodyLimit(contentType)
	if err != nil {
		return Request{}, err
	}
	if finite {
		return ParseFinite(raw)
	}
	return Parse(raw)
}

// ParseFinite accepts only a profiled run carrying exactly one valid io object,
// within the profile's 2 MiB body. It grants no execution or helper authority.
func ParseFinite(raw []byte) (Request, error) { return parseRequest(raw, finiteio.MaxBody, true) }

// ResponseLimit is the complete encoded response and retained receipt bound for
// one typed request. Only a valid io object raises it, to MaxResponseBytes; an
// unrepresentable io object yields zero so every response refuses.
func ResponseLimit(r Request) int {
	if r.IO != nil {
		n, err := finiteio.MaxResponseBytes(*r.IO)
		if err != nil || r.Operation != "run" {
			return 0
		}
		return n
	}
	if Growth(r.Operation) {
		return MaxBody
	}
	return MaxResponse
}

// validFiniteResult binds an observation to its exact profiled run request. It
// checks byte spelling, digest and bound only: wire data cannot prove that the
// producer fenced, drained and sealed the execution before publishing it.
func validFiniteResult(response Response, request Request) bool {
	var observed *finiteio.Output
	var run *RunOutcome
	switch r := response.Result.(type) {
	case *RunOutcome:
		if r != nil {
			run, observed = r, r.IO
		}
	case *SetupOutcome:
		if r != nil && r.IO != nil {
			return false
		}
	}
	if request.IO == nil || observed == nil {
		// A successful profiled run has no meaning without its exact observation.
		return observed == nil && (request.IO == nil || !response.OK)
	}
	if request.Operation != "run" || run.Execution == nil || run.Execution.Phase != "stopped" || run.ExitCode == nil {
		return false
	}
	if response.Error != nil && oneOf(response.Error.Code, "in_progress", "unavailable", "operation_in_doubt") {
		return false
	}
	return observed.Validate(*request.IO) == nil
}
