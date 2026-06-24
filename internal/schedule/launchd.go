package schedule

import (
	"fmt"
	"path/filepath"
	"strings"
)

// launchdPlistName is the basename of the LaunchAgent plist icebeam installs.
const launchdPlistName = Label + ".plist"

// LaunchdPlistPath returns the path to icebeam's LaunchAgent plist under the
// user's home directory (~/Library/LaunchAgents). The LaunchAgent location is a
// macOS convention and is independent of icebeam's XDG config/state layout.
func LaunchdPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdPlistName)
}

// GenerateLaunchd renders the LaunchAgent plist for the given spec. A friendly
// interval becomes StartInterval (hourly) or StartCalendarInterval (daily/
// weekly). A raw OnCalendar expression has no launchd equivalent and is rejected.
func GenerateLaunchd(spec Spec) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	if spec.Schedule.Calendar != "" {
		return "", fmt.Errorf("launchd does not support a raw calendar expression; use --interval (%s) on macOS", intervalList())
	}

	args := []string{spec.BinaryPath, "run"}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	b.WriteString("\t<key>Label</key>\n")
	b.WriteString("\t<string>" + plistEscape(Label) + "</string>\n")

	b.WriteString("\t<key>ProgramArguments</key>\n")
	b.WriteString("\t<array>\n")
	for _, a := range args {
		b.WriteString("\t\t<string>" + plistEscape(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")

	writeLaunchdEnvironment(&b, spec)

	writeLaunchdSchedule(&b, spec.Schedule.Interval)

	b.WriteString("\t<key>RunAtLoad</key>\n")
	b.WriteString("\t<false/>\n")

	if spec.LogPath != "" {
		b.WriteString("\t<key>StandardOutPath</key>\n")
		b.WriteString("\t<string>" + plistEscape(spec.LogPath) + "</string>\n")
		b.WriteString("\t<key>StandardErrorPath</key>\n")
		b.WriteString("\t<string>" + plistEscape(spec.LogPath) + "</string>\n")
	}

	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")

	return b.String(), nil
}

// writeLaunchdEnvironment pins icebeam's XDG base dirs in the agent's
// environment so the scheduled run resolves the same config/log as the user that
// installed it, regardless of the launchd session's HOME. Omitted when unset.
func writeLaunchdEnvironment(b *strings.Builder, spec Spec) {
	env := [][2]string{}
	if spec.XDGConfigHome != "" {
		env = append(env, [2]string{"XDG_CONFIG_HOME", spec.XDGConfigHome})
	}
	if spec.XDGStateHome != "" {
		env = append(env, [2]string{"XDG_STATE_HOME", spec.XDGStateHome})
	}
	if len(env) == 0 {
		return
	}

	b.WriteString("\t<key>EnvironmentVariables</key>\n")
	b.WriteString("\t<dict>\n")
	for _, kv := range env {
		b.WriteString("\t\t<key>" + plistEscape(kv[0]) + "</key>\n")
		b.WriteString("\t\t<string>" + plistEscape(kv[1]) + "</string>\n")
	}
	b.WriteString("\t</dict>\n")
}

// writeLaunchdSchedule writes the schedule keys for the given friendly interval:
// hourly uses StartInterval (3600s); daily/weekly use StartCalendarInterval.
func writeLaunchdSchedule(b *strings.Builder, interval Interval) {
	switch interval {
	case Hourly:
		b.WriteString("\t<key>StartInterval</key>\n")
		b.WriteString("\t<integer>3600</integer>\n")
	case Weekly:
		// Weekday 0 (Sunday) at 00:00.
		b.WriteString("\t<key>StartCalendarInterval</key>\n")
		b.WriteString("\t<dict>\n")
		b.WriteString("\t\t<key>Weekday</key>\n")
		b.WriteString("\t\t<integer>0</integer>\n")
		b.WriteString("\t\t<key>Hour</key>\n")
		b.WriteString("\t\t<integer>0</integer>\n")
		b.WriteString("\t\t<key>Minute</key>\n")
		b.WriteString("\t\t<integer>0</integer>\n")
		b.WriteString("\t</dict>\n")
	default: // Daily and any unset interval.
		// Every day at 00:00.
		b.WriteString("\t<key>StartCalendarInterval</key>\n")
		b.WriteString("\t<dict>\n")
		b.WriteString("\t\t<key>Hour</key>\n")
		b.WriteString("\t\t<integer>0</integer>\n")
		b.WriteString("\t\t<key>Minute</key>\n")
		b.WriteString("\t\t<integer>0</integer>\n")
		b.WriteString("\t</dict>\n")
	}
}

// plistEscape escapes the characters that are significant in plist XML text.
func plistEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return r.Replace(s)
}
