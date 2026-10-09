//go:build unix

package channels

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openCaptureFile opens the dev capture file for appending. It never
// follows a symbolic link and refuses anything but a regular file owned by
// this process's user with no group or other permissions: the file holds
// message bodies (one-time codes), so it must not be readable by others or
// redirectable by whoever controls its directory.
func openCaptureFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("capture file: %s is a symbolic link", path)
		}
		return nil, fmt.Errorf("capture file: %w", err)
	}
	if err := checkCaptureFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func checkCaptureFile(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("capture file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("capture file: %s is not a regular file", f.Name())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("capture file: %s has mode %v; it must not be accessible to group or others (use 0600)", f.Name(), fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("capture file: %s is owned by another user", f.Name())
	}
	return nil
}
