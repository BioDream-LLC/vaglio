//go:build unix

package audit

import (
	"os"
	"syscall"
)

// lock takes an exclusive advisory lock on the log, waiting for another process to release it. The kernel drops it if
// the process dies, so a crash never leaves the log locked.
func lock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
