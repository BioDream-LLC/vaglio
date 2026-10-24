// Package fsx replaces files in place, the way every write in Vaglio does it (write a copy, sync, rename over), on Unix
// and on Windows alike.
package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Replace renames from over to. On Unix a rename replaces a file whoever has it open. Windows refuses while another
// handle has the file open (a reader of the users file, a policy being loaded), and the refusal ends when the reader
// closes, a moment later; so there it is tried again for up to two seconds before the error is returned.
func Replace(from, to string) error {
	err := os.Rename(from, to)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if !errors.Is(err, fs.ErrPermission) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
		if err = os.Rename(from, to); err == nil {
			return nil
		}
	}
	return err
}

// SyncDir makes a directory's entries durable: a file created or renamed into it survives a power cut, not only its
// contents. Windows has no way to sync a directory and needs none (NTFS journals its metadata): there it does nothing.
func SyncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// WriteFile replaces path with data so that a power cut leaves either the old file or the new one, whole: written to
// a temporary file beside it, synced, renamed over it, and the directory synced.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.new")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // gone after the rename; left only when something failed
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = Replace(tmp, path)
	}
	if err == nil {
		err = SyncDir(dir)
	}
	return err
}
