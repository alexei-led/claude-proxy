package managed

import (
	"errors"
	"os"
	"syscall"
)

type processWatch struct {
	queue  int
	exited bool
}

func watchProcess(pid int) (*processWatch, error) {
	if pid < 1 {
		return nil, errors.New("invalid client process")
	}
	queue, err := syscall.Kqueue()
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(queue)
	event := syscall.Kevent_t{Ident: uint64(pid), Filter: syscall.EVFILT_PROC, Flags: syscall.EV_ADD | syscall.EV_ONESHOT, Fflags: syscall.NOTE_EXIT}
	_, err = syscall.Kevent(queue, []syscall.Kevent_t{event}, nil, nil)
	if err != nil {
		_ = syscall.Close(queue)
		if errors.Is(err, syscall.ESRCH) {
			return nil, os.ErrProcessDone
		}
		return nil, err
	}
	return &processWatch{queue: queue}, nil
}

func (watch *processWatch) alive() (bool, error) {
	if watch.exited {
		return false, nil
	}
	events := make([]syscall.Kevent_t, 1)
	count, err := syscall.Kevent(watch.queue, nil, events, &syscall.Timespec{})
	if err != nil {
		return true, err
	}
	if count > 0 {
		if events[0].Flags&syscall.EV_ERROR != 0 {
			return true, errors.New("process watch failed")
		}
		if events[0].Fflags&syscall.NOTE_EXIT != 0 {
			watch.exited = true
		}
	}
	return !watch.exited, nil
}

func (watch *processWatch) close() { _ = syscall.Close(watch.queue) }
