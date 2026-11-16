//go:build !linux

package registrationobserve

import (
	"context"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/bench"
	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/serviceidentity"
)

func Observe(context.Context, bench.Store, serviceidentity.Authority, Request) (*Observation, error) {
	return nil, ErrObservation
}
