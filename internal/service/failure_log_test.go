package service

import (
	"bytes"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

// The pilot host's failed creates returned operation_failed and benchd logged
// nothing; the cause had to be inferred from sudo lines and the source.
func TestFailedOperationLogsItsCause(t *testing.T) {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(previous) })
	s, create, root := v2Fixture(t)
	create.Repositories[1].Repository = filepath.Join(root, "missing-repository")
	v2Error(t, s, v2Operator, create, "operation_failed")
	line := out.String()
	if !strings.Contains(line, `operation failed: api=v2 operation="create" request_id="create-1" key="card"`) || !strings.Contains(line, "code=operation_failed cause=") || strings.Contains(line, `cause=""`) {
		t.Fatalf("failure not logged: %q", line)
	}
}
