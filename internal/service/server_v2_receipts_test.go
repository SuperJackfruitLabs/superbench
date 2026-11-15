package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/protocolv2"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/workspaceops"
)

func TestV2HTTPRunningInterruptedAndCompletedReceiptsNeverRedispatch(t *testing.T) {
	s, create, _ := v2Fixture(t)
	create.Version = protocolv2.Version
	digest, err := create.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if cached, err := s.receipts.Claim(v2Operator, create.RequestID, digest); err != nil || cached != nil {
		t.Fatal("claim failed", err)
	}
	v2Error(t, s, v2Operator, create, "in_progress")
	if _, err := os.Lstat(filepath.Join(s.config.Store.Data, "benches", create.Key)); !os.IsNotExist(err) {
		t.Fatal("in-progress replay materialized target", err)
	}
	// A new receipt handle represents service restart; opening marks the abandoned
	// running receipt interrupted and cannot make its operation executable again.
	if err := s.receipts.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := openReceipts(s.config.Store.State)
	if err != nil {
		t.Fatal(err)
	}
	s.receipts = replacement
	t.Cleanup(func() { _ = replacement.Close() })
	v2Error(t, s, v2Operator, create, "operation_in_doubt")
	if len(v2Snapshot(t, s)) != 0 {
		t.Fatal("interrupted creation repeated")
	}
	if _, err := os.Lstat(filepath.Join(s.config.Store.Data, "benches", create.Key)); !os.IsNotExist(err) {
		t.Fatal("interrupted receipt created checkout", err)
	}
}

func TestV2HTTPReceiptCapacityBlocksGrowthButNotReconciliation(t *testing.T) {
	s, create, _ := v2Fixture(t)
	v2OK(t, s, v2Operator, create)
	leased, _ := v2OK(t, s, v2Operator, protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "capacity-owner", RequestID: "capacity-lease"})
	token := v2Mutation(t, leased).Grant.LeaseToken
	_, err := receiptTestDB(t, s).Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO receipts(owner,id,digest,state,body,status) SELECT 9999,'fill-'||x,'unused','done',X'7B7D',200 FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	before := v2Snapshot(t, s)
	count := v2ReceiptCount(t, s)
	v2Error(t, s, v2Operator, protocolv2.Request{Operation: "setup", Key: create.Key, Holder: "capacity-owner", LeaseToken: token, RequestID: "capacity-setup"}, "unavailable")
	extra := create
	extra.Key = "capacity-new"
	extra.RequestID = "capacity-create"
	v2Error(t, s, v2Operator, extra, "unavailable")
	if !reflect.DeepEqual(before, v2Snapshot(t, s)) || v2ReceiptCount(t, s) != count {
		t.Fatal("capacity refusal changed owner or allocated receipt")
	}
	if _, err := os.Lstat(filepath.Join(s.config.Store.Data, "benches", extra.Key)); !os.IsNotExist(err) {
		t.Fatal("capacity refusal created checkout", err)
	}
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "stop", Key: create.Key, LeaseToken: token})
	admin, _ := v2OK(t, s, v2Operator, protocolv2.Request{Operation: "stop_admin", Key: create.Key})
	if v2Mutation(t, admin).Grant.LeaseToken != token {
		t.Fatal("receipt pressure prevented administrative recovery")
	}
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "checkpoint", Key: create.Key, LeaseToken: token})
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "release", Key: create.Key, LeaseToken: token})
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "archive", Key: create.Key})
	if v2ReceiptCount(t, s) != count {
		t.Fatal("reconciliation reserved growth receipt")
	}
}

func TestV2HTTPReceiptFinishFailureRetainsBoundedIndependentlyOwnedGrant(t *testing.T) {
	s, create, _ := v2Fixture(t)
	v2OK(t, s, v2Operator, create)
	if _, err := receiptTestDB(t, s).Exec(`CREATE TRIGGER fail_completion BEFORE UPDATE OF state ON receipts WHEN NEW.state='done' BEGIN SELECT RAISE(FAIL,'injected receipt completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	request := protocolv2.Request{Operation: "lease", Key: create.Key, Holder: "retained-owner", RequestID: "completion-failure"}
	response, status, body := v2Call(t, s, v2Operator, request)
	if status != 503 || response.Error == nil || response.Error.Code != "operation_in_doubt" || len(body) > protocolv2.MaxBody {
		t.Fatal("receipt save ambiguity was not bounded", status, string(body))
	}
	result := v2Mutation(t, response)
	if result.Grant == nil || result.RootPath == "" || result.Workspace.Lease == nil || result.Workspace.Lease.Holder != request.Holder {
		t.Fatal("receipt failure hid independently acquired owner")
	}
	if bytes.Contains(body, []byte("injected receipt completion failure")) {
		t.Fatal("raw persistence error leaked")
	}
	if err := s.config.Store.CheckWorkspaceOwner(create.Key, result.Grant.LeaseToken, request.Holder); err != nil {
		t.Fatal("returned capability does not own retained core lease", err)
	}
	before := v2Snapshot(t, s)
	v2Error(t, s, v2Operator, request, "operation_in_doubt")
	if !reflect.DeepEqual(before, v2Snapshot(t, s)) {
		t.Fatal("failed receipt completion redispatched growth")
	}
	// Reconciliation works even while every new completion would fail.
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "checkpoint", Key: create.Key, LeaseToken: result.Grant.LeaseToken})
	v2OK(t, s, v2Operator, protocolv2.Request{Operation: "release", Key: create.Key, LeaseToken: result.Grant.LeaseToken})
}

func TestV2HTTPMissingPartialCreationReceiptDoesNotResumeInitialization(t *testing.T) {
	s, create, _ := v2Fixture(t)
	create.Version = protocolv2.Version
	id, err := workspaceops.CreationID(create, v2Operator)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := s.config.Store.StageWorkspaceContext(context.Background(), id, creationForV2(create))
	if err != nil {
		t.Fatal(err)
	}
	if partial.Lease == nil || partial.Workspace.Phase != "initializing" {
		t.Fatal("partial creation fixture lost initialization owner")
	}
	before := v2Snapshot(t, s)
	files := v2Files(t, partial.Path)
	response := v2Error(t, s, v2Operator, create, "operation_in_doubt")
	if response.Result != nil || !reflect.DeepEqual(before, v2Snapshot(t, s)) || !reflect.DeepEqual(files, v2Files(t, partial.Path)) {
		t.Fatal("missing partial receipt resumed initialization or exported token")
	}
	if err := s.config.Store.CheckWorkspaceOwner(create.Key, partial.Lease.Token, "initialization"); err != nil {
		t.Fatal("partial owner changed", err)
	}
}
