package logging

import "os"

// IsTerminal reports whether the given file is attached to a terminal. It uses
// the file mode's character-device bit, which is sufficient to distinguish an
// interactive session from a scheduler (launchd/systemd) where stderr is a pipe
// or regular file. This avoids pulling in a dependency for TTY detection.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
