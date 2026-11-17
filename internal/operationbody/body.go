// Package operationbody bounds and classifies one v2 operation request body.
// It grants no authority: the service still authenticates the Unix peer,
// admits the connection, reserves the receipt and authorizes the operation.
package operationbody

import (
	"io"
	"mime"
	"net/http"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

// Read applies the media check and the body bound. With finite disabled it is
// exactly the legacy crossing: any application/json media, 64 KiB, and no
// meaning for a profile parameter. Enabled, only the exact finite profile
// raises the bound to 2 MiB; a malformed or unknown profile is refused.
func Read(w http.ResponseWriter, r *http.Request, finiteEnabled bool) (body []byte, finite bool, status int, message string) {
	limit := protocolv2.MaxBody
	contentType := r.Header.Get("Content-Type")
	if finiteEnabled {
		var err error
		if limit, finite, err = protocolv2.BodyLimit(contentType); err != nil {
			return nil, false, 415, "JSON required"
		}
	} else if media, _, err := mime.ParseMediaType(contentType); err != nil || media != "application/json" {
		return nil, false, 415, "JSON required"
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, int64(limit)))
	if err != nil {
		return nil, false, 413, "request body exceeds limit"
	}
	return body, finite, 0, ""
}

// Parse keeps exact profile↔io agreement. A profiled body must be a run
// carrying io; an unprofiled body is the unchanged legacy Parse, which
// refuses io. Neither path selects a helper, credential or execution scope.
func Parse(body []byte, finite bool) (protocolv2.Request, error) {
	if finite {
		return protocolv2.ParseFinite(body)
	}
	return protocolv2.Parse(body)
}

// Profiled reports the lookup profile under the same gate. Its request body
// keeps the 64 KiB bound; only the retained original body bound changes.
func Profiled(r *http.Request, finiteEnabled bool) (finite bool, ok bool) {
	contentType := r.Header.Get("Content-Type")
	if !finiteEnabled {
		media, _, err := mime.ParseMediaType(contentType)
		return false, err == nil && media == "application/json"
	}
	_, finite, err := protocolv2.BodyLimit(contentType)
	return finite, err == nil
}
