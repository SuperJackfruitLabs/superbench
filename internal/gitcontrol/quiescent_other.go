//go:build !linux

package gitcontrol

// Managed registration observation requires the Linux physical custody gate.
type Quiescence struct{}

func Quiescent(string) (*Quiescence, error) { return nil, quietRefusal(nil) }
func (*Quiescence) Recheck() error          { return quietRefusal(nil) }
func (*Quiescence) Close() error            { return nil }
