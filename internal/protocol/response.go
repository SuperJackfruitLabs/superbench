package protocol

type APIError struct {
	CommandExit int    `json:"-"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

type Response struct {
	Version   string    `json:"version"`
	Operation string    `json:"operation,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	OK        bool      `json:"ok"`
	Result    any       `json:"result,omitempty"`
	Error     *APIError `json:"error,omitempty"`
}

func (e *APIError) ExitCode() int {
	if e.CommandExit > 0 && e.CommandExit < 256 {
		return e.CommandExit
	}
	return 1
}
