//go:build !windows

package launcher

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

func isAddrInUse(err error) bool {
	return err != nil && (errors.Is(err, syscall.EADDRINUSE) || strings.Contains(strings.ToLower(err.Error()), "address already in use"))
}

func terminateProcess(p *os.Process) error {
	if p == nil {
		return nil
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return p.Kill()
	}
	return nil
}
