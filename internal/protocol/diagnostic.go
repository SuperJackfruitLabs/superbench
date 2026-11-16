package protocol

import "errors"

// shapeDiagnostic contains only names from the fixed v1 schema, never wire values.
type shapeDiagnostic string

func (e shapeDiagnostic) Error() string { return string(e) }

func unexpectedField(operation, field string) error {
	// Reflect only recognized schema names. Unknown field names can contain secrets.
	known := field == "version" || field == "operation"
	for _, s := range shapes {
		for _, name := range append(append([]string{}, s.required...), s.optional...) {
			if name == field {
				known = true
			}
		}
	}
	if !Known(operation) {
		return shapeDiagnostic("invalid operation request")
	}
	if !known {
		return shapeDiagnostic("unexpected field for operation " + operation)
	}
	return shapeDiagnostic("unexpected field " + field + " for operation " + operation)
}

// InvalidMessage returns a bounded, safe diagnostic for a rejected v1 request.
// Other parser errors remain generic; arbitrary error text must never reach peers.
func InvalidMessage(err error) string {
	var diagnostic shapeDiagnostic
	if errors.As(err, &diagnostic) {
		return string(diagnostic)
	}
	return "invalid operation request"
}
