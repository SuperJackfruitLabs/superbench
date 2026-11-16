package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseMatchesReadConfiguration(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("[setup]\nonce = 'echo ready'\n[caches]\nnpm = false\n"), []byte("unknown = true\n"), []byte("[archive]\nwhen = 'invalid'\n"), {0xff}} {
		t.Run(string(raw), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".bench"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".bench/config.toml"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			fromFile, fileErr := Read(root)
			fromBytes, parseErr := Parse(raw)
			if (fileErr == nil) != (parseErr == nil) || !reflect.DeepEqual(fromFile, fromBytes) {
				t.Fatalf("parser differs: file=%+v %v bytes=%+v %v", fromFile, fileErr, fromBytes, parseErr)
			}
			if fileErr != nil && fileErr.Error() != parseErr.Error() {
				t.Fatalf("parser error differs: %v != %v", fileErr, parseErr)
			}
		})
	}
}
