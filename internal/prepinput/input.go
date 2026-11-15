package prepinput

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/setup"
)

// Inputs bind preparation success to the parsed config and supported dependency
// manifests. Read through a checkout root, without following special files.
func Read(path string) (*setup.Config, string, error) {
	c, err := setup.Read(path)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	h := sha256.New()
	if c != nil {
		io.WriteString(h, c.Digest)
	}
	for _, name := range []string{"package.json", "pnpm-lock.yaml", "package-lock.json"} {
		io.WriteString(h, "\x00"+name+"\x00")
		f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if os.IsNotExist(e) {
			io.WriteString(h, "absent")
			continue
		}
		if e != nil {
			return nil, "", e
		}
		info, e := f.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
			f.Close()
			return nil, "", errors.New("preparation inputs must be regular files of at most 16 MiB")
		}
		sum := sha256.New()
		n, e := io.Copy(sum, io.LimitReader(f, (16<<20)+1))
		f.Close()
		if e != nil || n > 16<<20 {
			return nil, "", errors.New("preparation input cannot be read within its bound")
		}
		h.Write(sum.Sum(nil))
	}
	return c, hex.EncodeToString(h.Sum(nil)), nil
}
