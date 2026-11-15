//go:build linux

package hoststorage

import (
	"testing"
)

func TestRetirementRejectsNonzeroOpenUnlinkedProjectUsageAndAmbiguousKernelRows(t *testing.T) {
	for _, unit := range []string{"blocks", "inodes"} {
		t.Run(unit, func(t *testing.T) {
			for _, bad := range []string{"", "#10000 1 0 128", "#10000 invalid 0 128", "#10000 0 0 128\n#10000 0 0 128", "#9999 0 0 128"} {
				if verifyRetirementUsage([]byte(bad), 10000) == nil {
					t.Fatal("nonempty/unknown usage accepted", bad)
				}
			}
			if err := verifyRetirementUsage([]byte("#10000 0 0 128"), 10000); err != nil {
				t.Fatal(err)
			}
		})
	}
}
