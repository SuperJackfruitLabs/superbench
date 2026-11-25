// Package remotesftp serves SFTP confined to one workspace root.
//
// Every operation goes through an *os.Root, so no path, ".." or symbolic link
// reaches a file outside the root. An SFTP path is cleaned as path.Clean("/"+p)
// and "/" is the root itself. Ownership changes and hard links are refused.
package remotesftp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"
)

// RefuseRoot refuses to serve as uid 0: bench-sftp runs only after the drop
// to the payload account.
func RefuseRoot(uid int) error {
	if uid == 0 {
		return errors.New("bench-sftp refuses to run as root")
	}
	return nil
}

// Serve answers SFTP requests on rw until the client closes it.
func Serve(rw io.ReadWriteCloser, root *os.Root) error {
	h := &handlers{root: root}
	server := sftp.NewRequestServer(rw, sftp.Handlers{FileGet: h, FilePut: h, FileCmd: h, FileList: h})
	err := server.Serve()
	server.Close()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return nil
	}
	return err
}

type handlers struct{ root *os.Root }

// rel maps an SFTP path onto a name relative to the root; "/" is ".".
func rel(p string) string {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" {
		return "."
	}
	return name
}

// refusal turns os.Root's escape error, which the standard library does not
// export, into permission denied; any other error keeps its own status.
func refusal(err error) error {
	var inner error
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		inner = pe.Err
	case errors.As(err, &le):
		inner = le.Err
	}
	if inner != nil && inner.Error() == "path escapes from parent" {
		return sftp.ErrSSHFxPermissionDenied
	}
	return err
}

func (h *handlers) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	f, err := h.root.Open(rel(r.Filepath))
	if err != nil {
		return nil, refusal(err)
	}
	return f, nil
}

func (h *handlers) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	f, err := h.open(r, os.O_WRONLY)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (h *handlers) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	f, err := h.open(r, os.O_RDWR)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (h *handlers) open(r *sftp.Request, access int) (*os.File, error) {
	flags, p := access, r.Pflags()
	if p.Append {
		flags |= os.O_APPEND
	}
	if p.Creat {
		flags |= os.O_CREATE
	}
	if p.Trunc {
		flags |= os.O_TRUNC
	}
	if p.Excl {
		flags |= os.O_EXCL
	}
	perm := os.FileMode(0o666)
	if r.AttrFlags().Permissions {
		perm = os.FileMode(r.Attributes().Mode) & 0o777
	}
	f, err := h.root.OpenFile(rel(r.Filepath), flags, perm)
	if err != nil {
		return nil, refusal(err)
	}
	return f, nil
}

func (h *handlers) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		return refusal(h.setstat(r))
	case "Rename":
		// SFTP rename does not replace an existing target.
		if _, err := h.root.Lstat(rel(r.Target)); err == nil {
			return refusal(&fs.PathError{Op: "rename", Path: r.Target, Err: fs.ErrExist})
		} else if !errors.Is(err, fs.ErrNotExist) {
			return refusal(err)
		}
		return refusal(h.root.Rename(rel(r.Filepath), rel(r.Target)))
	case "Mkdir":
		return refusal(h.root.Mkdir(rel(r.Filepath), 0o777))
	case "Rmdir", "Remove":
		info, err := h.root.Lstat(rel(r.Filepath))
		if err != nil {
			return refusal(err)
		}
		if info.IsDir() != (r.Method == "Rmdir") {
			errno := syscall.EISDIR
			if r.Method == "Rmdir" {
				errno = syscall.ENOTDIR
			}
			return &fs.PathError{Op: strings.ToLower(r.Method), Path: r.Filepath, Err: errno}
		}
		return refusal(h.root.Remove(rel(r.Filepath)))
	case "Symlink":
		// The target is stored verbatim; os.Root never follows it out.
		return refusal(h.root.Symlink(r.Filepath, rel(r.Target)))
	case "Link":
		return sftp.ErrSSHFxPermissionDenied
	}
	return sftp.ErrSSHFxOpUnsupported
}

// PosixRename replaces an existing target, as posix-rename@openssh.com asks.
func (h *handlers) PosixRename(r *sftp.Request) error {
	return refusal(h.root.Rename(rel(r.Filepath), rel(r.Target)))
}

func (h *handlers) setstat(r *sftp.Request) error {
	flags, attrs, name := r.AttrFlags(), r.Attributes(), rel(r.Filepath)
	if flags.UidGid {
		return sftp.ErrSSHFxPermissionDenied
	}
	if flags.Size {
		f, err := h.root.OpenFile(name, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		err = f.Truncate(int64(attrs.Size))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	if flags.Permissions {
		// Permission bits only: setuid, setgid and sticky are never set.
		if err := h.root.Chmod(name, os.FileMode(attrs.Mode)&0o777); err != nil {
			return err
		}
	}
	if flags.Acmodtime {
		if err := h.root.Chtimes(name, time.Unix(int64(attrs.Atime), 0), time.Unix(int64(attrs.Mtime), 0)); err != nil {
			return err
		}
	}
	return nil
}

func (h *handlers) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	name := rel(r.Filepath)
	switch r.Method {
	case "List":
		return h.list(name)
	case "Stat":
		info, err := h.root.Stat(name)
		if err != nil {
			return nil, refusal(err)
		}
		return listing{info}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

func (h *handlers) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	info, err := h.root.Lstat(rel(r.Filepath))
	if err != nil {
		return nil, refusal(err)
	}
	return listing{info}, nil
}

func (h *handlers) Readlink(p string) (string, error) {
	target, err := h.root.Readlink(rel(p))
	return target, refusal(err)
}

// list reads names from the open directory and stats each through the root,
// never by a path outside it.
func (h *handlers) list(name string) (sftp.ListerAt, error) {
	dir, err := h.root.Open(name)
	if err != nil {
		return nil, refusal(err)
	}
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	out := make(listing, 0, len(names))
	for _, entry := range names {
		info, err := h.root.Lstat(path.Join(name, entry))
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed since the read
		}
		if err != nil {
			return nil, refusal(err)
		}
		out = append(out, info)
	}
	return out, nil
}

type listing []os.FileInfo

func (l listing) ListAt(buf []os.FileInfo, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative listing offset %d", offset)
	}
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(buf, l[offset:])
	if offset+int64(n) == int64(len(l)) {
		return n, io.EOF
	}
	return n, nil
}
