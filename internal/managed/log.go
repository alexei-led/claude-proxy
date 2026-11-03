package managed

import (
	"errors"
	"os"
	"sync"
	"syscall"
)

const maxLogBytes = 1 << 20

func openLog(path string) (*os.File, error) {
	// #nosec G304 -- The private runtime path is owner-validated; reject final symlinks.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, errors.New("cannot open private managed log")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("unsafe managed log")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		_ = file.Close()
		return nil, errors.New("managed log has a different owner")
	}
	return file, nil
}

// boundedLog keeps the current log and one previous file, each at most
// maxLogBytes. A full log becomes path.1, so the records before a rollover,
// such as startup and the first errors, survive it.
type boundedLog struct {
	mu   sync.Mutex
	path string
	file *os.File
}

func (sink *boundedLog) Write(body []byte) (int, error) {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	original := len(body)
	if len(body) > maxLogBytes {
		body = body[len(body)-maxLogBytes:]
	}
	info, err := sink.file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size()+int64(len(body)) > maxLogBytes {
		if err := sink.rotate(); err != nil {
			return 0, err
		}
	}
	if _, err := sink.file.Write(body); err != nil {
		return 0, err
	}
	return original, nil
}

// rotate renames the full log to path.1, replacing an older one, and opens a
// fresh file. Raw process output, such as a crash trace, still goes to the
// descriptor inherited at start. If a step fails, the current file is
// emptied instead, which keeps the size bound.
func (sink *boundedLog) rotate() error {
	if os.Rename(sink.path, sink.path+".1") == nil {
		if fresh, err := openLog(sink.path); err == nil {
			_ = sink.file.Close()
			sink.file = fresh
			return nil
		}
	}
	return sink.file.Truncate(0)
}

// Close closes the current file.
func (sink *boundedLog) Close() error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.file.Close()
}
