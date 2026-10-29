package relay

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// O_NOFOLLOW and fstat validate the opened inode, not a racy pre-open path.
// O_NONBLOCK prevents a named pipe from hanging startup before the type check.
func readPrivate(path string, secret bool, limit int64) ([]byte, error) {
	// #nosec G304 -- The operator selects this path; the opened inode is validated below.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open private file")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, errors.New("cannot stat private file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) || !info.Mode().IsRegular() {
		return nil, errors.New("unsafe private file")
	}
	mode := info.Mode().Perm()
	if secret {
		if mode != 0o400 && mode != 0o600 {
			return nil, errors.New("unsafe secret permissions")
		}
	} else if mode&0o022 != 0 {
		return nil, errors.New("unsafe configuration permissions")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("cannot read bounded private file")
	}
	return body, nil
}
