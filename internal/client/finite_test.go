package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

func TestV2ClientSendsFiniteProfileAndAcceptsOnlyItsBoundObservation(t *testing.T) {
	raw, err := os.ReadFile("../protocolv2/testdata/private-execution-io.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Request  json.RawMessage `json:"request"`
		Response struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		} `json:"response"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	request, err := protocolv2.ParseFinite(corpus.Request)
	if err != nil {
		t.Fatal(err)
	}
	var media []string
	var sent [][]byte
	socket := unixService(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		media, sent = append(media, r.Header.Get("Content-Type")), append(sent, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(corpus.Response.Status)
		_, _ = w.Write(corpus.Response.Body)
	})
	response, err := CallV2(context.Background(), socket, request)
	var failure *WorkspaceError
	if !errors.As(err, &failure) || failure.ExitCode() != 7 {
		t.Fatal("finite failure lost its command exit", err)
	}
	outcome, ok := response.Result.(*protocolv2.RunOutcome)
	if !ok || outcome.IO == nil || outcome.IO.StdoutBase64 != "AAEC" {
		t.Fatalf("finite observation lost: %#v", response.Result)
	}
	canonical, _ := json.Marshal(request)
	if media[0] != protocolv2.FiniteIOContentType || !bytes.Equal(sent[0], canonical) {
		t.Fatalf("finite request not sent under its profile: %q %s", media[0], sent[0])
	}
	// The same reply to the unprofiled form of the request is refused: io is
	// bound to the profiled identity and the legacy request bytes are unchanged.
	request.IO = nil
	if _, err = CallV2(context.Background(), socket, request); !errors.Is(err, protocolv2.ErrResponse) {
		t.Fatal("unprofiled request accepted an io observation", err)
	}
	if media[1] != "application/json" || bytes.Contains(sent[1], []byte(`"io"`)) {
		t.Fatalf("legacy request changed: %q %s", media[1], sent[1])
	}
}
