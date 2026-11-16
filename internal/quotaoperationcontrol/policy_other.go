//go:build !linux

package quotaoperationcontrol

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/hoststorage"
)

type policyPin struct{}

func installedPolicy() (*policyPin, hoststorage.Policy, error) {
	return nil, hoststorage.Policy{}, ErrPolicy
}
func (*policyPin) check(context.Context) error { return ErrPolicy }
func (*policyPin) close()                      {}
