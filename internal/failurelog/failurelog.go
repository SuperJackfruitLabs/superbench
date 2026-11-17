// Package failurelog writes the private cause of a failed operation to benchd's
// log (the service journal). Clients still receive only the static error their
// protocol defines (internal/scalarapi, internal/workspaceapi). A line carries
// the request's identifiers, the workspace phase and the error chain, never the
// request body, lease tokens, command arguments or run output; repository URL
// credentials and queries are removed from the cause.
package failurelog

import (
	"log"
	"regexp"
	"strconv"
	"unicode/utf8"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// MaxCause bounds the logged error text.
const MaxCause = 1024

var (
	userinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@"']*@`)
	query    = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s?#"']*)[?#][^\s"']*`)
)

// V1 logs a failed v1 (scalar bench) operation.
func V1(r protocol.Request, code string, err error) {
	write("v1", r.Operation, r.RequestID, r.Key, "", code, err)
}

// V2 logs a failed v2 operation with the workspace phase its result reports.
func V2(r protocolv2.Request, result protocolv2.Result, code string, err error) {
	write("v2", r.Operation, r.RequestID, r.Key, phase(result), code, err)
}

// Receipt logs a failure to record an operation's outcome. The client receives
// operation_in_doubt; the cause is only here.
func Receipt(api, operation, requestID, key string, err error) {
	write(api, operation, requestID, key, "", "receipt_save_failed", err)
}

func write(api, operation, requestID, key, phase, code string, err error) {
	if err == nil {
		return
	}
	if phase == "" {
		phase = "-"
	}
	log.Printf("operation failed: api=%s operation=%s request_id=%s key=%s phase=%s code=%s cause=%s",
		api, strconv.Quote(operation), strconv.Quote(requestID), strconv.Quote(key), strconv.Quote(phase), code, Cause(err))
}

// Cause is the bounded, quoted, credential-free text of err.
func Cause(err error) string {
	text := userinfo.ReplaceAllString(err.Error(), "${1}[redacted]@")
	text = query.ReplaceAllString(text, "${1}")
	if len(text) > MaxCause {
		cut := MaxCause
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "...[truncated]"
	}
	return strconv.Quote(text)
}

func phase(result protocolv2.Result) string {
	switch r := result.(type) {
	case *protocolv2.Mutation:
		if r != nil {
			return r.Workspace.Phase
		}
	case *protocolv2.RunOutcome:
		if r != nil {
			return r.Workspace.Phase
		}
	case *protocolv2.SetupOutcome:
		if r != nil {
			return r.Workspace.Phase
		}
	}
	return ""
}
