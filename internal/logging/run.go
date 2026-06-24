package logging

import (
	"time"
)

// LogStart records a command run's start: the command name and its arguments.
// Callers must pass arguments that already exclude secrets (passwords are never
// passed as args in icebeam; they reach restic via files/env), but sensitive
// attribute keys are redacted defensively by the handler regardless.
func (l *Logger) LogStart(command string, args []string) {
	l.Info("run start",
		"command", command,
		"args", args,
	)
}

// LogEnd records a command run's outcome and duration. A nil err is reported as
// a success; a non-nil err is logged at error level with its message. The error
// is not returned to the caller — this is a logging helper, not error handling.
func (l *Logger) LogEnd(command string, duration time.Duration, err error) {
	if err != nil {
		l.Error("run end",
			"command", command,
			"duration", duration.String(),
			"outcome", "failure",
			"error", err.Error(),
		)
		return
	}

	l.Info("run end",
		"command", command,
		"duration", duration.String(),
		"outcome", "success",
	)
}
