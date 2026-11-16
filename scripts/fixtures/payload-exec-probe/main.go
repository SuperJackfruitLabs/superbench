//go:build linux

// This standalone CGO0 observation process is executed by the real helper. It
// grants no authority and reports actual kernel credentials and descriptor IDs.
package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type descriptor struct {
	FD     int
	Device uint64
	Inode  uint64
	Mode   uint32
	Target string
}
type observation struct {
	Status      map[string]string
	Descriptors []descriptor
}

func main() {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil || len(raw) > 65536 {
		os.Exit(2)
	}
	result := observation{Status: map[string]string{}}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			switch key {
			case "Uid", "Gid", "Groups", "CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb", "NoNewPrivs":
				if _, exists := result.Status[key]; exists {
					os.Exit(2)
				}
				result.Status[key] = strings.Join(strings.Fields(value), " ")
			}
		}
	}
	directory, err := os.Open("/proc/self/fd")
	if err != nil {
		os.Exit(2)
	}
	scanFD := int(directory.Fd())
	entries, err := directory.Readdirnames(257)
	if err != nil && err != io.EOF || len(entries) > 256 {
		os.Exit(2)
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry)
		if err != nil {
			os.Exit(2)
		}
		// Only our exact, still-held directory scanner is excluded. Any other
		// disappearing descriptor or failed target read is a failed observation.
		if fd == scanFD {
			continue
		}
		var info syscall.Stat_t
		if err := syscall.Fstat(fd, &info); err != nil {
			os.Exit(2)
		}
		target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		if err != nil {
			os.Exit(2)
		}
		result.Descriptors = append(result.Descriptors, descriptor{fd, uint64(info.Dev), info.Ino, info.Mode, target})
	}
	if err := directory.Close(); err != nil {
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(2)
	}
}
