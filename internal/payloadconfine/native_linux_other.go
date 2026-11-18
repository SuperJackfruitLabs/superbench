//go:build linux && !amd64 && !arm64

package payloadconfine

// Other architectures have no reviewed filter: the payload launch refuses.
func native() (Arch, uintptr, bool) { return Arch{}, 0, false }
