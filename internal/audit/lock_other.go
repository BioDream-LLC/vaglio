//go:build !unix && !windows

package audit

import (
	"errors"
	"os"
)

// Vaglio's audit log needs an exclusive file lock across processes, which this build has no way to take with the standard
// library alone. Rather than risk two writers numbering the same record, it refuses to write.
func lock(*os.File) error {
	return errors.New("audit: this platform has no file lock; run Vaglio on Linux, macOS or Windows")
}

func unlock(*os.File) {}
