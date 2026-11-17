package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/activation"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/freshreadiness"
)

// serve must check the real ready-journal prerequisite, before any socket.
func TestServeRefusesFreshPayloadWithoutAReadyIndexedJournal(t *testing.T) {
	if reflect.ValueOf(freshStorage).Pointer() != reflect.ValueOf(freshreadiness.Storage).Pointer() || reflect.ValueOf(freshIdentity).Pointer() != reflect.ValueOf(freshreadiness.Identity).Pointer() {
		t.Fatal("serve does not use the installed fresh_payload prerequisites")
	}
	identity, storage := freshIdentity, freshStorage
	t.Cleanup(func() { freshIdentity, freshStorage = identity, storage })
	freshIdentity = func() error { return nil }
	freshStorage = func() error { return errors.New("journal has no ready record") }
	c, _ := serviceFixture(t, true, false)
	c.Activation = activation.Switches{FreshPayload: true, Cancellation: true}
	if err := Serve(context.Background(), c); err == nil || !strings.Contains(err.Error(), "indexed quota operation journal: journal has no ready record") {
		t.Fatal("fresh_payload served without a ready journal", err)
	}
}
