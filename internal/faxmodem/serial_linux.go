package faxmodem

import (
	"fmt"
	"syscall"
)

const (
	getTermios = syscall.TCGETS
	setTermios = syscall.TCSETS
	crtscts    = 0x80000000 // CRTSCTS
)

func setSpeed(t *syscall.Termios, baud int) error {
	speeds := map[int]uint32{19200: syscall.B19200, 38400: syscall.B38400, 57600: syscall.B57600, 115200: syscall.B115200}
	s, ok := speeds[baud]
	if !ok {
		return fmt.Errorf("baud %d: 19200, 38400, 57600 or 115200", baud)
	}
	t.Cflag &^= 0x100f // CBAUD
	t.Cflag |= s
	t.Ispeed, t.Ospeed = s, s
	return nil
}
