//go:build windows

package audit

import (
	"os"
	"syscall"
	"unsafe"
)

// The standard library has no LockFileEx, so it is called from kernel32 directly. Windows drops the lock when the
// handle closes or the process dies, so a crash never leaves the log locked, as with flock on Unix.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const lockfileExclusiveLock = 0x2 // LOCKFILE_EXCLUSIVE_LOCK; without LOCKFILE_FAIL_IMMEDIATELY it waits

// Windows locks are mandatory: a locked byte stops every other handle reading it. So the lock is one byte far past any
// log's end (offset 2^62), which serves as a mutex between processes and never stands in the way of a reader.
const lockOffsetHigh = 0x40000000

// lock takes the exclusive lock, waiting for another process to release it.
func lock(f *os.File) error {
	ol := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
	r, _, err := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r == 0 {
		return os.NewSyscallError("LockFileEx", err)
	}
	return nil
}

func unlock(f *os.File) {
	ol := syscall.Overlapped{OffsetHigh: lockOffsetHigh}
	procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
}
