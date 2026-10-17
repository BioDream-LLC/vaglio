//go:build !linux && !darwin

package faxmodem

import (
	"errors"
	"os"
)

// OpenPort is not available here: a fax modem is supported on Linux and macOS only.
func OpenPort(device string, baud int) (*os.File, error) {
	return nil, errors.New("a fax modem is supported on Linux and macOS only")
}
