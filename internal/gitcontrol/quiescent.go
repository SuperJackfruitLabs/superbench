package gitcontrol

import "errors"

var ErrQuiescent = errors.New("Git control journal is not safely quiescent")

type quiescenceError struct{ cause error }

func (e *quiescenceError) Error() string   { return ErrQuiescent.Error() }
func (e *quiescenceError) Unwrap() []error { return []error{ErrQuiescent, e.cause} }

func quietRefusal(cause error) error { return &quiescenceError{cause: cause} }
