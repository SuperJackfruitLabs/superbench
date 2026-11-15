//go:build !linux

package hoststorage

import (
	"context"
	"errors"
)

func (Policy) RetireApproved(context.Context, RetirementApproval) (Assignment, error) {
	return Assignment{}, errors.New("managed quota retirement requires Linux")
}
func (Policy) VerifyRetiredStorage(context.Context, RetirementApproval) (Assignment, error) {
	return Assignment{}, errors.New("managed quota retirement requires Linux")
}

func (Policy) VerifyRetirementAuthority(context.Context, RetirementApproval) (Assignment, error) {
	return Assignment{}, errors.New("managed storage authority verification requires Linux")
}
