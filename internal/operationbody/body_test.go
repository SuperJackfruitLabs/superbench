package operationbody

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/finiteio"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
)

const legacyRun = `{"version":"superbench.v2","operation":"run","key":"card","holder":"run:one","request_id":"run-1","arguments":["worker"]}`

func finiteRun(stdin []byte) string {
	return strings.TrimSuffix(legacyRun, "}") + `,"io":{"version":"superbench.finite-io.v1","stdin_base64":"` + base64.StdEncoding.EncodeToString(stdin) + `","stdout_limit_bytes":16}}`
}
func classify(t *testing.T, enabled bool, media, body string) (protocolv2.Request, int, error) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v2/operations", strings.NewReader(body))
	r.Header.Set("Content-Type", media)
	raw, finite, status, _ := Read(httptest.NewRecorder(), r, enabled)
	if status != 0 {
		return protocolv2.Request{}, status, nil
	}
	request, err := Parse(raw, finite)
	return request, 0, err
}

func TestDisabledGateIsTheUnchangedLegacyCrossing(t *testing.T) {
	for _, media := range []string{"application/json", protocolv2.FiniteIOContentType, "application/json; profile=unknown"} {
		if request, status, err := classify(t, false, media, legacyRun); status != 0 || err != nil || request.IO != nil {
			t.Fatal("legacy body changed", media, status, err)
		}
		if _, status, err := classify(t, false, media, finiteRun([]byte{0})); status != 0 || !errors.Is(err, protocolv2.ErrInvalid) {
			t.Fatal("disabled gate accepted io", media, status, err)
		}
	}
	if _, status, _ := classify(t, false, protocolv2.FiniteIOContentType, finiteRun(make([]byte, 64<<10))); status != 413 {
		t.Fatal("disabled gate raised the 64 KiB body bound", status)
	}
	if _, status, _ := classify(t, false, "text/plain", legacyRun); status != 415 {
		t.Fatal("non-JSON media accepted", status)
	}
	r := httptest.NewRequest("POST", "/v2/receipts/lookup", nil)
	r.Header.Set("Content-Type", protocolv2.FiniteIOContentType)
	if finite, ok := Profiled(r, false); finite || !ok {
		t.Fatal("disabled gate honored the lookup profile")
	}
	if finite, ok := Profiled(r, true); !finite || !ok {
		t.Fatal("enabled gate ignored the lookup profile")
	}
}

func TestEnabledGateRequiresExactProfileAndIOAgreementWithinBounds(t *testing.T) {
	large := finiteRun(make([]byte, finiteio.MaxBytes))
	if request, status, err := classify(t, true, protocolv2.FiniteIOContentType, large); status != 0 || err != nil || request.IO == nil || len(large) <= protocolv2.MaxBody {
		t.Fatal("largest finite input refused", status, err)
	}
	if request, status, err := classify(t, true, "application/json", legacyRun); status != 0 || err != nil || request.IO != nil {
		t.Fatal("unprofiled legacy run changed under the gate", status, err)
	}
	for name, c := range map[string]struct {
		media, body string
		status      int
	}{
		"io-without-profile":   {"application/json", finiteRun([]byte{0}), 0},
		"profile-without-io":   {protocolv2.FiniteIOContentType, legacyRun, 0},
		"profile-on-setup":     {protocolv2.FiniteIOContentType, strings.Replace(finiteRun([]byte{0}), `"run"`, `"setup"`, 1), 0},
		"decoded-input-over":   {protocolv2.FiniteIOContentType, finiteRun(make([]byte, finiteio.MaxBytes+1)), 0},
		"body-over-2mib":       {protocolv2.FiniteIOContentType, large + strings.Repeat(" ", finiteio.MaxBody-len(large)+1), 413},
		"unknown-profile":      {"application/json; profile=unknown", finiteRun([]byte{0}), 415},
		"duplicate-profile":    {protocolv2.FiniteIOContentType + "; profile=superbench.finite-io.v1", finiteRun([]byte{0}), 415},
		"unprofiled-over-64ki": {"application/json", finiteRun(make([]byte, 64<<10)), 413},
	} {
		request, status, err := classify(t, true, c.media, c.body)
		if status != c.status || c.status == 0 && !errors.Is(err, protocolv2.ErrInvalid) || request.IO != nil {
			t.Fatal("finite refusal", name, status, err)
		}
	}
	exact := large + strings.Repeat(" ", finiteio.MaxBody-len(large))
	if _, status, err := classify(t, true, protocolv2.FiniteIOContentType, exact); status != 0 || err != nil || !bytes.HasSuffix([]byte(exact), []byte(" ")) {
		t.Fatal("exact 2 MiB profiled body refused", status, err)
	}
}
