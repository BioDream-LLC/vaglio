//go:build linux || darwin

package faxmodem

import (
	"os"
	"syscall"
	"unsafe"
)

// OpenPort opens a serial port raw (8 data bits, no parity, hardware flow control, the modem's lines ignored until the
// call), at baud. Opened non-blocking, so closing it ends a read that is waiting.
func OpenPort(device string, baud int) (*os.File, error) {
	f, err := os.OpenFile(device, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	var t syscall.Termios
	if err := ioctl(f.Fd(), getTermios, unsafe.Pointer(&t)); err != nil {
		f.Close()
		return nil, os.NewSyscallError("tcgetattr", err)
	}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON | syscall.IXOFF
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8 | syscall.CREAD | syscall.CLOCAL | crtscts
	t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
	if err := setSpeed(&t, baud); err != nil {
		f.Close()
		return nil, err
	}
	if err := ioctl(f.Fd(), setTermios, unsafe.Pointer(&t)); err != nil {
		f.Close()
		return nil, os.NewSyscallError("tcsetattr", err)
	}
	return f, nil
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}
