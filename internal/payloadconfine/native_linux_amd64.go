//go:build linux && amd64

package payloadconfine

func native() (Arch, uintptr, bool) { return AMD64, 317, true } // SYS_SECCOMP
