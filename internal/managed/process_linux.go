package managed

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

type processWatch struct {
	pid   int
	birth string
}

func watchProcess(pid int) (*processWatch, error) {
	birth, alive, err := processIdentity(pid)
	if err != nil {
		return nil, err
	}
	if !alive {
		return nil, os.ErrProcessDone
	}
	return &processWatch{pid: pid, birth: birth}, nil
}

func processIdentity(pid int) (string, bool, error) {
	if pid < 1 {
		return "", false, errors.New("invalid client process")
	}
	// #nosec G304 -- Numeric PIDs are registered by local same-user clients.
	body, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	end := strings.LastIndexByte(string(body), ')')
	if end < 0 {
		return "", false, errors.New("invalid process state")
	}
	fields := strings.Fields(string(body[end+1:]))
	if len(fields) < 20 {
		return "", false, errors.New("incomplete process state")
	}
	return fields[19], fields[0] != "Z" && fields[0] != "X", nil
}

func (watch *processWatch) alive() (bool, error) {
	birth, alive, err := processIdentity(watch.pid)
	return alive && birth == watch.birth, err
}

func (*processWatch) close() {}
