package operationreceipts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/operationcontrol"
)

func TestCancelScopeLinearizesLaunchAndRevokesContext(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 4, RequestID: "launch", Digest: strings.Repeat("a", 64)}, Key: "card", Operation: "run", Epoch: strings.Repeat("b", 32)}
	if _, err = r.claim(4, "launch", i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	ctx, closeScope, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { done <- operationcontrol.Effect(ctx, func() error { close(entered); <-release; return nil }) }()
	<-entered
	canceled := make(chan error, 1)
	go func() {
		state, e := r.requestCancel(4, "launch", i.Target.Digest)
		if e == nil && state != "cancel_requested" {
			e = errors.New("false completion")
		}
		canceled <- e
	}()
	select {
	case e := <-canceled:
		t.Fatal("cancel acknowledged before in-flight launch boundary ended", e)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = <-canceled; err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("durable cancel did not notify long-running operation")
	}
	called := false
	if err = operationcontrol.Effect(ctx, func() error { called = true; return nil }); !errors.Is(err, errCancelRequested) || called {
		t.Fatal("post-cancel launch ran", err)
	}
	if err = r.finish(4, "launch", []byte("grant"), 200); !errors.Is(err, errCancelRequested) {
		t.Fatal("post-cancel grant saved", err)
	}
}
func TestScopeRequiresExactActiveAdmissionAndCannotDuplicate(t *testing.T) {
	r, err := openReceipts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	i := operationcontrol.Intent{Target: operationcontrol.Target{UID: 1, RequestID: "scope", Digest: strings.Repeat("c", 64)}, Key: "card", Operation: "create", Epoch: strings.Repeat("d", 32)}
	if _, _, err = r.operationScope(context.Background(), i); err == nil {
		t.Fatal("unadmitted scope accepted")
	}
	if _, err = r.claim(1, "scope", i.Target.Digest); err != nil {
		t.Fatal(err)
	}
	_, closeScope, err := r.operationScope(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	if _, _, err = r.operationScope(context.Background(), i); err == nil {
		t.Fatal("second controller accepted")
	}
	other := i
	other.Target.Digest = strings.Repeat("e", 64)
	if _, _, err = r.operationScope(context.Background(), other); !errors.Is(err, errConflict) {
		t.Fatal("digest mismatch accepted", err)
	}
}
