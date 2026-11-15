package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/execution"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocol"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspace"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

const v2Operator = uint32(101)
const v2OtherOperator = uint32(102)
const v2Reader = uint32(201)

func v2Fixture(t *testing.T) (*Server, protocolv2.Request, string) {
	t.Helper()
	c, root := serviceFixture(t, true, false)
	c.Operators = map[uint32]bool{v2Operator: true, v2OtherOperator: true}
	c.Readers = map[uint32]bool{v2Reader: true}
	if err := c.Store.Migrate(); err != nil {
		t.Fatal(err)
	}
	receipts, err := openReceipts(c.Store.State)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipts.db.Close() })
	t.Cleanup(func() {
		_ = filepath.WalkDir(c.Store.Data, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink == 0 {
				return os.Chmod(path, 0700)
			}
			return nil
		})
	})
	s := &Server{config: c, ctx: context.Background(), receipts: receipts, slots: make(chan struct{}, 8)}
	initialize := true
	request := protocolv2.Request{Version: protocolv2.Version, Operation: "create", Key: "card", CheckpointRepository: "a", Initialize: &initialize, RequestID: "create-1"}
	for _, name := range []string{"a", "b"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		request.Repositories = append(request.Repositories, protocolv2.Repository{Name: name, Repository: gitFixture(t, path)})
	}
	return s, request, root
}
func v2Raw(t *testing.T, s *Server, uid uint32, method, path, media string, body []byte) (int, []byte) {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), uidKey{}, uid))
	request.Header.Set("Content-Type", media)
	request.Header.Set("Authorization", "Bearer forged-authority")
	request.Header.Set("X-Operator-UID", "101")
	recorder := httptest.NewRecorder()
	s.ServeHTTP(recorder, request)
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatal("missing bounded response headers")
	}
	return recorder.Code, recorder.Body.Bytes()
}
func v2Call(t *testing.T, s *Server, uid uint32, request protocolv2.Request) (protocolv2.Response, int, []byte) {
	t.Helper()
	request.Version = protocolv2.Version
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	status, body := v2Raw(t, s, uid, "POST", "/v2/operations", "application/json", raw)
	response, err := protocolv2.ParseResponse(body, status, request)
	if err != nil {
		t.Fatalf("service emitted invalid v2 response HTTP%d: %s", status, body)
	}
	return response, status, body
}
func v2OK(t *testing.T, s *Server, uid uint32, request protocolv2.Request) (protocolv2.Response, []byte) {
	t.Helper()
	response, status, body := v2Call(t, s, uid, request)
	if status != 200 || !response.OK {
		t.Fatalf("operation %s failed HTTP%d: %s", request.Operation, status, body)
	}
	return response, body
}
func v2Error(t *testing.T, s *Server, uid uint32, request protocolv2.Request, code string) protocolv2.Response {
	t.Helper()
	response, _, body := v2Call(t, s, uid, request)
	if response.OK || response.Error == nil || response.Error.Code != code {
		t.Fatalf("wanted %s: %s", code, body)
	}
	return response
}
func v2Mutation(t *testing.T, response protocolv2.Response) *protocolv2.Mutation {
	t.Helper()
	switch result := response.Result.(type) {
	case *protocolv2.Mutation:
		return result
	case *protocolv2.RunOutcome:
		return &result.Mutation
	case *protocolv2.SetupOutcome:
		return &result.Mutation
	}
	t.Fatal("missing mutation result")
	return nil
}
func v2Snapshot(t *testing.T, s *Server) []bench.Bench {
	t.Helper()
	rows, err := s.config.Store.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func v2Files(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256(raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func v2ReceiptCount(t *testing.T, s *Server) int {
	t.Helper()
	var n int
	if err := s.receipts.db.QueryRow("SELECT count(*) FROM receipts").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestV2HTTPWholeLifecycleMixedStatusAndHistoricalReplay(t *testing.T) {
	s, create, _ := v2Fixture(t)
	created, createBytes := v2OK(t, s, v2Operator, create)
	initial := v2Mutation(t, created)
	if initial.Workspace.MemberCount != 2 || initial.Workspace.Phase != "ready" || initial.Grant != nil || initial.RootPath == "" {
		t.Fatal("creation did not return complete unleased workspace")
	}
	if _, err := s.config.Store.CreateColdContext(context.Background(), "aaa", bench.Options{Repo: create.Repositories[0].Repository}, false); err != nil {
		t.Fatal(err)
	}
	lease := protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "first-owner", RequestID: "lease-1"}
	leased, leaseBytes := v2OK(t, s, v2Operator, lease)
	owner := v2Mutation(t, leased)
	if owner.Grant == nil || owner.Workspace.Lease == nil || owner.Workspace.Lease.Holder != lease.Holder || owner.RootPath != initial.RootPath {
		t.Fatal("new owner has no independent capability/root")
	}
	token := owner.Grant.LeaseToken
	status, statusBytes := v2OK(t, s, v2Reader, protocolv2.Request{Operation: "status"})
	inventory := status.Result.(*protocolv2.Status)
	if len(inventory.Benches) != 2 {
		t.Fatal("mixed inventory omitted a kind")
	}
	if _, ok := inventory.Benches[0].(*protocolv2.ScalarInventory); !ok {
		t.Fatal("scalar registration omitted")
	}
	workspace, ok := inventory.Benches[1].(*protocolv2.WorkspaceInventory)
	if !ok || len(workspace.Members) != 2 || workspace.Lease == nil {
		t.Fatal("aggregate inventory incomplete")
	}
	for _, private := range []string{token, initial.RootPath, create.Repositories[0].Repository, create.Repositories[1].Repository, `"root_path"`, `"lease_token"`, `"continuation"`, `"repository"`, `"log"`, `"arguments"`, `"environment"`} {
		if bytes.Contains(statusBytes, []byte(private)) {
			t.Fatal("reader status leaked private field", private)
		}
	}
	v2Error(t, s, v2Operator, protocolv2.Request{Operation: "setup", Key: create.Key, Holder: "wrong-owner", LeaseToken: token, RequestID: "wrong-holder"}, "forbidden")
	before := v2Snapshot(t, s)
	if _, _, replay := v2Call(t, s, v2Operator, lease); !bytes.Equal(replay, leaseBytes) {
		t.Fatal("live owner receipt failed exact replay")
	}
	if !reflect.DeepEqual(before, v2Snapshot(t, s)) {
		t.Fatal("receipt replay reacquired owner")
	}
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "checkpoint", Key: create.Key, LeaseToken: token})
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "release", Key: create.Key, LeaseToken: token})
	next := protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "second-owner", RequestID: "lease-2"}
	nextResponse, _ := v2OK(t, s, v2Operator, next)
	nextToken := v2Mutation(t, nextResponse).Grant.LeaseToken
	if nextToken == token {
		t.Fatal("new lease reused old capability")
	}
	before = v2Snapshot(t, s)
	for _, cached := range []struct {
		request protocolv2.Request
		body    []byte
	}{{create, createBytes}, {lease, leaseBytes}} {
		if _, _, replay := v2Call(t, s, v2Operator, cached.request); !bytes.Equal(replay, cached.body) {
			t.Fatal("historical response was rewritten by current holder")
		}
	}
	if !reflect.DeepEqual(before, v2Snapshot(t, s)) {
		t.Fatal("historical response changed current holder")
	}
	setup := protocolv2.Request{Operation: "setup", Key: create.Key, Holder: next.Holder, LeaseToken: nextToken, RepositoryName: "b", RequestID: "setup-1"}
	setupResponse, _ := v2OK(t, s, v2Operator, setup)
	setupResult := setupResponse.Result.(*protocolv2.SetupOutcome)
	if setupResult.Grant != nil || setupResult.Workspace.Lease != nil || len(setupResult.Preparation) != 1 || setupResult.Preparation[0].Name != "b" || setupResult.Preparation[0].Status != "prepared" {
		t.Fatal("selected setup did not checkpoint/release exact owner")
	}
	stoppedOwner, _ := v2OK(t, s, v2Operator, protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "third-owner", RequestID: "lease-3"})
	stopToken := v2Mutation(t, stoppedOwner).Grant.LeaseToken
	stopped, _ := v2OK(t, s, v2Operator, protocolv2.Request{Operation: "stop", Key: create.Key, LeaseToken: stopToken})
	if m := v2Mutation(t, stopped); m.Grant != nil || m.RootPath != "" || !m.Workspace.Lease.StopRequested {
		t.Fatal("ordinary stop leaked token or failed revocation")
	}
	admin, _ := v2OK(t, s, v2Operator, protocolv2.Request{Operation: "stop_admin", Key: create.Key})
	if m := v2Mutation(t, admin); m.Grant == nil || m.Grant.LeaseToken != stopToken || !m.Workspace.Lease.StopRequested {
		t.Fatal("verified admin stop did not recover exact retained owner")
	}
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "checkpoint", Key: create.Key, LeaseToken: stopToken})
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "release", Key: create.Key, LeaseToken: stopToken})
	archived, _ := v2OK(t, s, v2Operator, protocolv2.Request{Operation: "archive", Key: create.Key})
	if m := v2Mutation(t, archived); m.Workspace.Archive == nil || m.Workspace.Archive.FrozenAt == "" || m.Grant != nil {
		t.Fatal("archive did not freeze complete aggregate")
	}
	before = v2Snapshot(t, s)
	for _, cached := range []struct {
		request protocolv2.Request
		body    []byte
	}{{create, createBytes}, {lease, leaseBytes}} {
		if _, _, replay := v2Call(t, s, v2Operator, cached.request); !bytes.Equal(replay, cached.body) {
			t.Fatal("archive blocked historical receipt replay")
		}
	}
	if !reflect.DeepEqual(before, v2Snapshot(t, s)) {
		t.Fatal("cached replay changed archived state")
	}
}

func TestV2HTTPAuthorizationAndMalformedRequestsHaveNoCoreOrReceiptEffects(t *testing.T) {
	s, create, _ := v2Fixture(t)
	before := v2Snapshot(t, s)
	for _, uid := range []uint32{v2Reader, 999} {
		response := v2Error(t, s, uid, create, "forbidden")
		if response.Result != nil {
			t.Fatal("denied mutation returned private result")
		}
	}
	for _, body := range []string{`{}`, `{"version":"superbench.v2","operation":"status","operation":"stop_admin"}`, `{"version":"superbench.v2","operation":"status","lease_token":"PRIVATE-CANARY"}`, `{"version":"superbench.v2","operation":"create","key":"card","request_id":"bad","initialize":true,"checkpoint_repository":"a","repositories":[{"name":"a","name":"b","repository":"PRIVATE-CANARY"}]}`, `{"version":"superbench.v2","operation":"status"} {}`, `{"version":"superbench.v2","operation":"status","key":null}`} {
		status, raw := v2Raw(t, s, v2Operator, "POST", "/v2/operations", "application/json", []byte(body))
		response, err := protocolv2.ParseResponse(raw, status, protocolv2.Request{Operation: "status"})
		if err != nil || status != 400 || response.Operation != "" || response.RequestID != "" || response.Result != nil || bytes.Contains(raw, []byte("PRIVATE-CANARY")) {
			t.Fatal("malformed request reflected untrusted identity", status, string(raw), err)
		}
	}
	for _, test := range []struct {
		method, path, media string
		body                []byte
		want                int
	}{{"GET", "/v2/operations", "application/json", nil, 405}, {"POST", "/v2/nope", "application/json", nil, 404}, {"POST", "/v2/operations", "text/plain", nil, 415}, {"POST", "/v2/operations", "application/json", bytes.Repeat([]byte("x"), protocolv2.MaxBody+1), 413}} {
		status, raw := v2Raw(t, s, v2Operator, test.method, test.path, test.media, test.body)
		if _, err := protocolv2.ParseResponse(raw, status, protocolv2.Request{Operation: "status"}); err != nil || status != test.want {
			t.Fatal("invalid early transport response", status, string(raw), err)
		}
	}
	if !reflect.DeepEqual(before, v2Snapshot(t, s)) || v2ReceiptCount(t, s) != 0 {
		t.Fatal("rejected request changed core or reserved receipt")
	}
	v2OK(t, s, v2Reader, protocolv2.Request{Operation: "status"})
}

func TestV2HTTPCrossUIDBodyAndVersionReceiptConflicts(t *testing.T) {
	s, create, _ := v2Fixture(t)
	v2OK(t, s, v2Operator, create)
	before := v2Snapshot(t, s)
	other := v2Error(t, s, v2OtherOperator, create, "conflict")
	if other.Result != nil {
		t.Fatal("another UID adopted registered creation identity")
	}
	changed := create
	changed.Key = "different"
	v2Error(t, s, v2Operator, changed, "conflict")
	if !reflect.DeepEqual(before, v2Snapshot(t, s)) {
		t.Fatal("request conflict changed existing aggregate")
	}
	if _, err := s.config.Store.CreateColdContext(context.Background(), "scalar", bench.Options{Repo: create.Repositories[0].Repository}, false); err != nil {
		t.Fatal(err)
	}
	old := protocol.Request{Version: protocol.Version, Operation: "lease", Key: "scalar", Holder: "legacy", RequestID: "cross-version"}
	raw, _ := json.Marshal(old)
	status, body := v2Raw(t, s, v2Operator, "POST", "/v1/operations", "application/json", raw)
	if status != 200 {
		t.Fatal("v1 setup failed", string(body))
	}
	v2Error(t, s, v2Operator, protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "aggregate", RequestID: old.RequestID}, "conflict")
	v2Error(t, s, v2Operator, protocolv2.Request{Operation: "lease", Key: "scalar", Holder: "aggregate", RequestID: "wrong-kind"}, "conflict")
}

func TestV2HTTPNativeUnsupportedRunRetainsOnlyNewAuthorizedGrant(t *testing.T) {
	if execution.Supported {
		t.Skip("native non-Linux refusal boundary")
	}
	s, create, _ := v2Fixture(t)
	v2OK(t, s, v2Operator, create)
	request := protocolv2.Request{Operation: "run", Key: create.Key, Holder: "native-owner", Arguments: []string{"/bin/true"}, RequestID: "native-run"}
	response := v2Error(t, s, v2Operator, request, "operation_failed")
	result := response.Result.(*protocolv2.RunOutcome)
	if result.Grant == nil || result.Workspace.Lease == nil || result.Execution != nil || result.ExitCode != nil {
		t.Fatal("native refusal lost acquired owner or invented execution")
	}
	rows := v2Snapshot(t, s)
	if rows[0].Lease.Holder != request.Holder || rows[0].Lease.Execution != nil {
		t.Fatal("unsupported payload started or lost retained owner")
	}
}

func TestV2HTTPMissingCreationReceiptCannotRedispatchMatchingCoreIdentity(t *testing.T) {
	s, create, _ := v2Fixture(t)
	create.Version = protocolv2.Version
	id, err := workspaceops.CreationID(create, v2Operator)
	if err != nil {
		t.Fatal(err)
	}
	spec := creationForV2(create)
	if _, err := s.config.Store.InitializeWorkspaceContext(context.Background(), id, spec); err != nil {
		t.Fatal(err)
	}
	before := v2Snapshot(t, s)
	files := v2Files(t, s.config.Store.Data)
	response := v2Error(t, s, v2Operator, create, "operation_in_doubt")
	if response.Result != nil || !reflect.DeepEqual(before, v2Snapshot(t, s)) || !reflect.DeepEqual(files, v2Files(t, s.config.Store.Data)) {
		t.Fatal("missing receipt redispatched existing creation")
	}
}

func creationForV2(request protocolv2.Request) workspace.Spec {
	spec := workspace.Spec{Key: request.Key, CheckpointRepository: request.CheckpointRepository}
	for _, member := range request.Repositories {
		spec.Repositories = append(spec.Repositories, workspace.Repository{Name: member.Name, Repository: member.Repository, Branch: member.Branch, Base: member.Base})
	}
	return spec
}
