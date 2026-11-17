package stageexec

import (
	"bytes"
	"errors"
)

// MaxResultBytes bounds explicit opaque result capture independently of log size.
// It allows 1 MiB of payload stdout plus a fixed helper exec marker (<64 bytes).
const MaxResultBytes uint64 = 1<<20 + 64

var ErrResultTransport = errors.New("execution result requires prepared transport")

var ErrResultLimit = errors.New("execution result limit exceeds bound")
var ErrResultOverflow = errors.New("execution result exceeds bound")

type resultBuffer struct {
	data     bytes.Buffer
	limit    uint64
	overflow bool
}

func (b *resultBuffer) Write(p []byte) (int, error) {
	if uint64(len(p)) > b.limit-uint64(b.data.Len()) {
		b.overflow = true
		return 0, ErrResultOverflow
	}
	return b.data.Write(p)
}
func (b *resultBuffer) copy() []byte { return append([]byte{}, b.data.Bytes()...) }
