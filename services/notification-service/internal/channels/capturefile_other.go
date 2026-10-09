//go:build !unix

package channels

import (
	"errors"
	"os"
)

// openCaptureFile is unsupported where symlink-safe opening and ownership
// checks are unavailable.
func openCaptureFile(string) (*os.File, error) {
	return nil, errors.New("capture file: not supported on this platform")
}
