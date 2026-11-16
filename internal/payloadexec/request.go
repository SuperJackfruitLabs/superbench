package payloadexec

import (
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadwire"
	"regexp"
)

// Compatibility names retain the private request API and error identity.
const Version = payloadwire.Version
const MaxRequest = payloadwire.MaxRequest
const MaxPayloadInput = payloadwire.MaxPayloadInput

var ErrRequest = payloadwire.ErrRequest
var logical = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
var identity = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hostIdentity = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Scope = payloadwire.Scope
type Request = payloadwire.Request

func ParseRequest(raw []byte) (Request, error) { return payloadwire.ParseRequest(raw) }
