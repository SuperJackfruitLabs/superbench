//go:build !linux

package payloadsession

import (
	"errors"
	"os"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/payloadpolicy"
)

var ErrSession = errors.New("managed payload session refused")

// Session mode exists only on Linux; every entry point refuses elsewhere.
func VerifyStdio(uint32) error                            { return ErrSession }
func Receive(int, int, time.Duration) ([]*os.File, error) { return nil, ErrSession }
func VerifyDescriptors([]*os.File, bool, uint32) error    { return ErrSession }
func Enter(payloadpolicy.Policy, string, *os.File) error  { return ErrSession }
func Prepare([]*os.File, bool, uint32, uint32) error      { return ErrSession }
func Stdin() bool                                         { return false }
func Child() bool                                         { return false }

type Handoff struct {
	Path        string
	Argv, Env   []string
	PTY         bool
	Directory   string
	Device, Ino uint64
}

func Run([]*os.File, *os.File, Handoff, uint32, uint32, func() error) error { return ErrSession }
func Begin() (Handoff, error)                                               { return Handoff{}, ErrSession }
func Init(Handoff, func() error) error                                      { return ErrSession }
