// Package schedule generates and installs the OS-native scheduler unit that runs
// `icebeam run` on a recurring schedule: a launchd LaunchAgent plist on macOS or
// a systemd service+timer on Linux. The unit-generation layer is pure and
// host-independent so it can be tested without the host's real scheduler.
package schedule

import (
	"fmt"
	"strings"
)

// Label is the launchd label / systemd unit base name icebeam installs under.
const Label = "com.itspriddle.icebeam"

// Interval is a friendly recurrence shorthand translated into each scheduler's
// native expression.
type Interval string

// Supported friendly intervals.
const (
	Hourly Interval = "hourly"
	Daily  Interval = "daily"
	Weekly Interval = "weekly"
)

// validIntervals lists the friendly intervals, for error messages.
var validIntervals = []Interval{Hourly, Daily, Weekly}

// Schedule describes when the unit should fire. Exactly one of Interval or
// Calendar is set: Interval is a friendly shorthand; Calendar is a raw
// scheduler-native expression (a systemd OnCalendar string) passed through
// verbatim.
type Schedule struct {
	Interval Interval
	Calendar string
}

// NewSchedule builds a Schedule from the user's flags. interval is a friendly
// shorthand (hourly/daily/weekly); calendar is a raw OnCalendar expression. They
// are mutually exclusive; calendar takes precedence when both are empty-checked.
func NewSchedule(interval, calendar string) (Schedule, error) {
	calendar = strings.TrimSpace(calendar)
	interval = strings.TrimSpace(interval)

	if calendar != "" {
		if interval != "" {
			return Schedule{}, fmt.Errorf("specify either --interval or --calendar, not both")
		}
		return Schedule{Calendar: calendar}, nil
	}

	if interval == "" {
		interval = string(Daily)
	}

	iv := Interval(strings.ToLower(interval))
	for _, valid := range validIntervals {
		if iv == valid {
			return Schedule{Interval: iv}, nil
		}
	}
	return Schedule{}, fmt.Errorf("unknown --interval %q; valid values: %s", interval, intervalList())
}

// intervalList renders the supported intervals for an error message.
func intervalList() string {
	names := make([]string, len(validIntervals))
	for i, iv := range validIntervals {
		names[i] = string(iv)
	}
	return strings.Join(names, ", ")
}

// Describe returns a human label for the schedule, used in status/summary output.
func (s Schedule) Describe() string {
	if s.Calendar != "" {
		return s.Calendar
	}
	return string(s.Interval)
}

// Spec is everything the unit templates need: the resolved icebeam binary path,
// the XDG base directories the unit should resolve config/state against, and the
// schedule.
type Spec struct {
	// BinaryPath is the absolute path to the icebeam binary the unit invokes.
	BinaryPath string
	// XDGConfigHome is the XDG config base ($XDG_CONFIG_HOME) the unit should
	// resolve icebeam's config against. Pinned in the unit's environment so the
	// scheduled run finds the same config regardless of how the scheduler sets
	// HOME. Empty omits the override (config resolves via the unit's own HOME).
	XDGConfigHome string
	// XDGStateHome is the XDG state base ($XDG_STATE_HOME) for the log file.
	// Empty omits the override.
	XDGStateHome string
	// LogPath is the icebeam log file the unit's stdout/stderr is appended to.
	LogPath string
	// Schedule is when the unit fires.
	Schedule Schedule
}

// validate ensures the spec carries the paths the templates require.
func (s Spec) validate() error {
	if s.BinaryPath == "" {
		return fmt.Errorf("schedule: binary path is required")
	}
	return nil
}
