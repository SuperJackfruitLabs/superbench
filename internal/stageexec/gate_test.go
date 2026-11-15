package stageexec

import (
	"testing"
	"time"
)

func TestExecutionGateSerializesSameKeyButNotOtherKeys(t *testing.T) {
	state := t.TempDir()
	entered, release, second, other := make(chan struct{}), make(chan struct{}), make(chan error, 1), make(chan error, 1)
	go func() { second <- Gate(state, "card", func() error { close(entered); <-release; return nil }) }()
	<-entered
	blocked := make(chan error, 1)
	go func() { blocked <- Gate(state, "card", func() error { return nil }) }()
	go func() { other <- Gate(state, "other", func() error { return nil }) }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unrelated key blocked")
	}
	select {
	case err := <-blocked:
		t.Fatal("same-key gate escaped", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gate not released")
	}
}
