//go:build !windows

package endpoint

import (
	"errors"
	"syscall"
)

// connectionReset reports a peer reset, as each platform's sockets spell it.
func connectionReset(err error) bool { return errors.Is(err, syscall.ECONNRESET) }
