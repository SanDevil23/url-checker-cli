//go:build windows

package checker

import (
	"errors"
	"syscall"
)

// wsaeconnrefused is WSAECONNREFUSED. On Windows, syscall.ECONNREFUSED is a
// synthetic value used only by Go's fake-net build (js/wasip1) and never
// matches a real socket error, so we check the actual Winsock errno here
// instead of pulling in golang.org/x/sys/windows for one constant.
const wsaeconnrefused = syscall.Errno(10061)

// connRefused reports whether err indicates the connection was refused.
func connRefused(err error) bool {
	return errors.Is(err, wsaeconnrefused)
}
