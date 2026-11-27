//go:build !windows

package checker

import (
	"errors"
	"syscall"
)

// connRefused reports whether err indicates the connection was refused.
func connRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
