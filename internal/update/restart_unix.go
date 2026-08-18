//go:build unix

package update

import (
	"fmt"
	"os"
	"syscall"
)

// restartProcess replaces the current process with the (just-updated) binary
// at path, keeping the same PID, arguments and environment — so pidfiles and
// service managers keep working. syscall.Exec only returns on failure; the
// error is passed up so it reaches the log and the UI instead of the process
// silently vanishing (the bundled start.sh has no supervisor to notice).
func restartProcess(path string) error {
	args := append([]string{path}, os.Args[1:]...)
	if err := syscall.Exec(path, args, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", path, err)
	}
	return nil // unreachable
}
