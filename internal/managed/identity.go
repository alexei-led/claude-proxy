// Package managed owns same-user broker discovery and client-process lifetimes.
package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alexei-led/claude-proxy/internal/relay"
)

type location struct {
	config     string
	executable string
	directory  string
	identity   string
	socket     string
	lock       string
	log        string
}

func locate(path string, config *relay.Config) (location, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return location{}, errors.New("cannot resolve selected configuration")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return location{}, errors.New("cannot resolve selected configuration")
	}
	absolute = filepath.Join(parent, filepath.Base(absolute))
	executable, err := os.Executable()
	if err != nil {
		return location{}, errors.New("cannot identify installed executable")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return location{}, errors.New("installed executable is unavailable")
	}
	// #nosec G304 -- Hash the current executable, never a network-selected path.
	file, err := os.Open(executable)
	if err != nil {
		return location{}, errors.New("installed executable is unavailable")
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	_ = file.Close()
	if err != nil {
		return location{}, errors.New("cannot identify installed executable")
	}
	fingerprint := config.Fingerprint()
	_, _ = hash.Write([]byte(absolute))
	_, _ = hash.Write(fingerprint[:])
	identity := hex.EncodeToString(hash.Sum(nil)[:16])
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base, err = os.UserCacheDir()
		if err != nil {
			return location{}, errors.New("private runtime directory is unavailable")
		}
	}
	if !filepath.IsAbs(base) {
		return location{}, errors.New("runtime parent must be absolute")
	}
	directory := filepath.Join(base, "claude-proxy")
	if err := privateDirectory(directory); err != nil {
		return location{}, err
	}
	socket := filepath.Join(directory, identity+".sock")
	if len(socket) > 100 {
		return location{}, errors.New("runtime path is too long; select a short XDG_RUNTIME_DIR")
	}
	return location{config: absolute, executable: executable, directory: directory, identity: identity, socket: socket, lock: filepath.Join(directory, identity+".lock"), log: filepath.Join(directory, identity+".log")}, nil
}

func privateDirectory(path string) error {
	// #nosec G703 -- The operator selects the runtime parent; the broker directory is owner-validated.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.New("cannot prepare private runtime parent")
	}
	// #nosec G703 -- Create only the selected private runtime directory.
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("cannot create private runtime directory")
	}
	// #nosec G703 -- Inspect the selected runtime directory without following a final symlink.
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("unsafe runtime directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		return errors.New("runtime directory has a different owner")
	}
	return nil
}

func acquire(ctx context.Context, path string) (*os.File, error) {
	// #nosec G304 -- This is an owned runtime path; validate the opened inode.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, errors.New("cannot open broker startup lock")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("unsafe broker startup lock")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(stat.Uid) != int64(os.Getuid()) {
		_ = file.Close()
		return nil, errors.New("broker lock has a different owner")
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, errors.New("cannot acquire broker startup lock")
		}
		if err := pause(ctx, 25*time.Millisecond); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
}

func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.New("managed proxy startup canceled or timed out")
	case <-timer.C:
		return nil
	}
}
