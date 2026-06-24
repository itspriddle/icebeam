package schedule

import (
	"path/filepath"
	"strings"
)

// systemd unit base name and the service/timer file names icebeam installs.
const (
	systemdUnitName    = "icebeam"
	systemdServiceFile = systemdUnitName + ".service"
	systemdTimerFile   = systemdUnitName + ".timer"
)

// SystemdUnitDir returns the user-level systemd unit directory under the XDG
// config dir (~/.config/systemd/user), honoring the XDG config base passed in.
// xdgConfigHome is the base config directory (e.g. ~/.config), NOT icebeam's
// namespaced subdir, since systemd owns its own subtree there.
func SystemdUnitDir(xdgConfigHome string) string {
	return filepath.Join(xdgConfigHome, "systemd", "user")
}

// SystemdServicePath returns the path to the installed icebeam.service unit.
func SystemdServicePath(xdgConfigHome string) string {
	return filepath.Join(SystemdUnitDir(xdgConfigHome), systemdServiceFile)
}

// SystemdTimerPath returns the path to the installed icebeam.timer unit.
func SystemdTimerPath(xdgConfigHome string) string {
	return filepath.Join(SystemdUnitDir(xdgConfigHome), systemdTimerFile)
}

// GenerateSystemd renders the systemd service and timer unit files for the spec.
// A friendly interval maps to systemd's built-in OnCalendar shorthand
// (hourly/daily/weekly); a raw OnCalendar expression passes through verbatim.
func GenerateSystemd(spec Spec) (service, timer string, err error) {
	if err := spec.validate(); err != nil {
		return "", "", err
	}

	onCalendar := spec.Schedule.Calendar
	if onCalendar == "" {
		onCalendar = string(spec.Schedule.Interval)
	}

	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString("Description=icebeam restic backup run\n")
	sb.WriteString("\n")
	sb.WriteString("[Service]\n")
	sb.WriteString("Type=oneshot\n")
	// Pin icebeam's XDG base dirs so the scheduled run resolves the same
	// config/log as the user that installed it.
	if spec.XDGConfigHome != "" {
		sb.WriteString("Environment=XDG_CONFIG_HOME=" + spec.XDGConfigHome + "\n")
	}
	if spec.XDGStateHome != "" {
		sb.WriteString("Environment=XDG_STATE_HOME=" + spec.XDGStateHome + "\n")
	}
	sb.WriteString("ExecStart=" + spec.BinaryPath + " run\n")
	service = sb.String()

	var tb strings.Builder
	tb.WriteString("[Unit]\n")
	tb.WriteString("Description=Schedule icebeam restic backup run\n")
	tb.WriteString("\n")
	tb.WriteString("[Timer]\n")
	tb.WriteString("OnCalendar=" + onCalendar + "\n")
	// Run a missed timer (machine asleep/off at the scheduled time) on next boot.
	tb.WriteString("Persistent=true\n")
	tb.WriteString("\n")
	tb.WriteString("[Install]\n")
	tb.WriteString("WantedBy=timers.target\n")
	timer = tb.String()

	return service, timer, nil
}

// TimerUnitName is the systemd timer unit name (e.g. for `systemctl --user`
// enable/start/status commands).
func TimerUnitName() string {
	return systemdTimerFile
}

// ServiceUnitName is the systemd service unit name.
func ServiceUnitName() string {
	return systemdServiceFile
}
