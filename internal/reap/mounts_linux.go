//go:build linux

package reap

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

func mountPaths() ([]string, error) {
	f, e := os.Open("/proc/self/mountinfo")
	if e != nil {
		return nil, e
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	decode := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	var paths []string
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			return nil, errors.New("invalid mount table")
		}
		paths = append(paths, decode.Replace(fields[4]))
		if len(paths) > 100000 {
			return nil, errors.New("mount table exceeds limit")
		}
	}
	return paths, scanner.Err()
}
