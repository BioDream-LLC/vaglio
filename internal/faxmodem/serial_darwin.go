package faxmodem

import (
	"fmt"
	"syscall"
)

const (
	getTermios = syscall.TIOCGETA
	setTermios = syscall.TIOCSETA
	crtscts    = 0x00030000 // CCTS_OFLOW | CRTS_IFLOW
)

func setSpeed(t *syscall.Termios, baud int) error {
	switch baud {
	case 19200, 38400, 57600, 115200:
	default:
		return fmt.Errorf("baud %d: 19200, 38400, 57600 or 115200", baud)
	}
	t.Ispeed, t.Ospeed = uint64(baud), uint64(baud)
	return nil
}
