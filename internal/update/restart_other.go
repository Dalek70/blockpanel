//go:build !unix

package update

import "errors"

// Non-unix (Windows) builds cannot exec-in-place. Releases only target macOS
// and Linux; surface an honest error instead of killing the process.
func restartProcess(string) error {
	return errors.New("in-place restart is not supported on this platform — restart the panel manually")
}
