//go:build linux && arm64

package payloadconfine

func native() (Arch, uintptr, bool) { return ARM64, 277, true } // SYS_SECCOMP
