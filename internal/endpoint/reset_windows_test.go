package endpoint

import (
	"errors"
	"syscall"
)

// connectionReset reports a peer reset. Winsock reports WSAECONNRESET
// (10054), which Go's syscall.ECONNRESET placeholder does not match.
func connectionReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.Errno(10054))
}
