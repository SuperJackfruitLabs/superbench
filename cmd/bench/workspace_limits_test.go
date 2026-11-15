package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/reap"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspacearchive"
)

func TestWorkspaceRemoteOutputEscapingCannotExceedInventoryBound(t *testing.T) {
	// '<' is a valid branch byte. A compact valid remote inventory can fit the
	// transport bound while Go's HTML escaping makes its canonical form larger.
	var raw strings.Builder
	raw.WriteString(`{"version":"superbench.v2","operation":"status","ok":true,"result":{"benches":[`)
	for i := 0; i < 1000; i++ {
		if i > 0 {
			raw.WriteByte(',')
		}
		fmt.Fprintf(&raw, `{"kind":"scalar","key":"item%04d","branch":"%s","state":"active","quota_bytes":0,"quota_inodes":0}`, i, strings.Repeat("<", 255))
	}
	raw.WriteString(`]}}`)
	body := []byte(raw.String())
	request := protocolv2.Request{Version: protocolv2.Version, Operation: "status"}
	response, err := protocolv2.ParseResponse(body, 200, request)
	if err != nil || len(body) >= protocolv2.MaxResponse {
		t.Fatal("fixture is not a bounded valid remote inventory", err)
	}
	canonical, err := json.Marshal(response)
	if err != nil || len(canonical) <= protocolv2.MaxResponse {
		t.Fatal("fixture did not exercise escaping expansion", err)
	}
	socket := cliSocket(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	var stdout, stderr bytes.Buffer
	err = workspaceCLIContext(context.Background(), []string{"status", "--socket", socket}, &stdout, &stderr)
	if err == nil || stdout.Len() != 0 || !strings.Contains(err.Error(), "output limit") {
		t.Fatal("oversized canonical output partially escaped", err, stdout.Len())
	}
}

func TestWorkspaceExactLimitReviewedPlanOutputRemainsReadable(t *testing.T) {
	plan := &workspacearchive.Plan{Version: workspacearchive.PlanVersion, Key: "card", Digest: strings.Repeat("0", 64)}
	plan.Tree.Entries = []reap.Entry{}
	base, err := plan.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	entry := reap.Entry{Path: strings.Repeat("p", 240) + "00000", Mode: 0444, Device: 1, Inode: 100000, SHA256: strings.Repeat("a", 64), GitSHA1: strings.Repeat("b", 40), GitSHA256: strings.Repeat("c", 64)}
	row, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	count := (reap.MaxManifest - len(base) + 1) / (len(row) + 1)
	if count >= reap.MaxEntries {
		t.Fatal("fixture exceeds entry bound")
	}
	for i := 0; i < count; i++ {
		next := entry
		next.Path = strings.Repeat("p", 240) + fmt.Sprintf("%05d", i)
		plan.Tree.Entries = append(plan.Tree.Entries, next)
	}
	encoded, err := plan.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	remaining := reap.MaxManifest - len(encoded)
	for i := 0; remaining > 0; i++ {
		extra := min(10, remaining)
		plan.Tree.Entries[i].Path += strings.Repeat("x", extra)
		remaining -= extra
	}
	plan.Digest, err = plan.DigestValue()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := writeWorkspacePlan(&stdout, plan); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != reap.MaxManifest {
		t.Fatal("fixture did not reach exact manifest limit", stdout.Len())
	}
	if _, err := workspacearchive.ReadPlan(stdout.Bytes()); err != nil {
		t.Fatal("CLI emitted an unreadable reviewed plan", err)
	}
}
