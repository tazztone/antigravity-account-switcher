//go:build windows

package launcher

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

func isAddrInUse(err error) bool {
	return err != nil && (errors.Is(err, syscall.Errno(10048)) ||
		strings.Contains(strings.ToLower(err.Error()), "address already in use") ||
		strings.Contains(strings.ToLower(err.Error()), "only one usage of each socket address"))
}

func terminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	return p.Kill()
}
