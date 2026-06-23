package schedule

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleSpec() Spec {
	return Spec{
		BinaryPath:    "/usr/local/bin/icebeam",
		XDGConfigHome: "/home/josh/.config",
		XDGStateHome:  "/home/josh/.local/state",
		LogPath:       "/home/josh/.local/state/icebeam/icebeam.log",
		Schedule:      Schedule{Interval: Daily},
	}
}

func TestNewSchedule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		interval string
		calendar string
		want     Schedule
		wantErr  string
	}{
		{name: "empty defaults to daily", want: Schedule{Interval: Daily}},
		{name: "hourly", interval: "hourly", want: Schedule{Interval: Hourly}},
		{name: "daily", interval: "daily", want: Schedule{Interval: Daily}},
		{name: "weekly", interval: "weekly", want: Schedule{Interval: Weekly}},
		{name: "case-insensitive", interval: "DAILY", want: Schedule{Interval: Daily}},
		{name: "raw calendar", calendar: "*-*-* 03:00:00", want: Schedule{Calendar: "*-*-* 03:00:00"}},
		{name: "unknown interval", interval: "fortnightly", wantErr: "unknown --interval"},
		{name: "both set", interval: "daily", calendar: "*-*-* 03:00:00", wantErr: "not both"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewSchedule(tt.interval, tt.calendar)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGenerateLaunchdDaily(t *testing.T) {
	t.Parallel()

	plist, err := GenerateLaunchd(sampleSpec())
	require.NoError(t, err)

	assert.Contains(t, plist, "<?xml")
	assert.Contains(t, plist, "<key>Label</key>")
	assert.Contains(t, plist, "<string>"+Label+"</string>")
	// The unit invokes the resolved binary with `run`.
	assert.Contains(t, plist, "<string>/usr/local/bin/icebeam</string>")
	assert.Contains(t, plist, "<string>run</string>")
	// Daily fires at 00:00 via StartCalendarInterval, not StartInterval.
	assert.Contains(t, plist, "<key>StartCalendarInterval</key>")
	assert.NotContains(t, plist, "<key>StartInterval</key>")
	// XDG bases are pinned so the scheduled run finds the same config/log.
	assert.Contains(t, plist, "<key>XDG_CONFIG_HOME</key>")
	assert.Contains(t, plist, "<string>/home/josh/.config</string>")
	assert.Contains(t, plist, "<key>XDG_STATE_HOME</key>")
	// The log is captured for the agent's stdout/stderr.
	assert.Contains(t, plist, "<key>StandardOutPath</key>")
	assert.Contains(t, plist, "/home/josh/.local/state/icebeam/icebeam.log")
}

func TestGenerateLaunchdHourlyUsesStartInterval(t *testing.T) {
	t.Parallel()

	spec := sampleSpec()
	spec.Schedule = Schedule{Interval: Hourly}

	plist, err := GenerateLaunchd(spec)
	require.NoError(t, err)

	assert.Contains(t, plist, "<key>StartInterval</key>")
	assert.Contains(t, plist, "<integer>3600</integer>")
	assert.NotContains(t, plist, "<key>StartCalendarInterval</key>")
}

func TestGenerateLaunchdWeeklyUsesWeekday(t *testing.T) {
	t.Parallel()

	spec := sampleSpec()
	spec.Schedule = Schedule{Interval: Weekly}

	plist, err := GenerateLaunchd(spec)
	require.NoError(t, err)

	assert.Contains(t, plist, "<key>StartCalendarInterval</key>")
	assert.Contains(t, plist, "<key>Weekday</key>")
}

func TestGenerateLaunchdRejectsRawCalendar(t *testing.T) {
	t.Parallel()

	spec := sampleSpec()
	spec.Schedule = Schedule{Calendar: "*-*-* 03:00:00"}

	_, err := GenerateLaunchd(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launchd does not support a raw calendar")
}

func TestGenerateLaunchdRequiresBinary(t *testing.T) {
	t.Parallel()

	spec := sampleSpec()
	spec.BinaryPath = ""

	_, err := GenerateLaunchd(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binary path is required")
}

func TestGenerateSystemdInterval(t *testing.T) {
	t.Parallel()

	service, timer, err := GenerateSystemd(sampleSpec())
	require.NoError(t, err)

	// Service runs the resolved binary once and exits.
	assert.Contains(t, service, "Type=oneshot")
	assert.Contains(t, service, "ExecStart=/usr/local/bin/icebeam run")
	assert.Contains(t, service, "Environment=XDG_CONFIG_HOME=/home/josh/.config")
	assert.Contains(t, service, "Environment=XDG_STATE_HOME=/home/josh/.local/state")

	// Timer translates the friendly interval to OnCalendar and survives reboots.
	assert.Contains(t, timer, "OnCalendar=daily")
	assert.Contains(t, timer, "Persistent=true")
	assert.Contains(t, timer, "WantedBy=timers.target")
}

func TestGenerateSystemdRawCalendar(t *testing.T) {
	t.Parallel()

	spec := sampleSpec()
	spec.Schedule = Schedule{Calendar: "*-*-* 03:00:00"}

	_, timer, err := GenerateSystemd(spec)
	require.NoError(t, err)

	// A raw OnCalendar expression passes through verbatim.
	assert.Contains(t, timer, "OnCalendar=*-*-* 03:00:00")
}

func TestGenerateSystemdRequiresBinary(t *testing.T) {
	t.Parallel()

	spec := sampleSpec()
	spec.BinaryPath = ""

	_, _, err := GenerateSystemd(spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "binary path is required")
}

func TestUnitPaths(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"/home/josh/Library/LaunchAgents/"+launchdPlistName,
		LaunchdPlistPath("/home/josh"),
	)
	assert.Equal(t,
		"/home/josh/.config/systemd/user/icebeam.service",
		SystemdServicePath("/home/josh/.config"),
	)
	assert.Equal(t,
		"/home/josh/.config/systemd/user/icebeam.timer",
		SystemdTimerPath("/home/josh/.config"),
	)
}

func TestScheduleDescribe(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "daily", Schedule{Interval: Daily}.Describe())
	assert.Equal(t, "*-*-* 03:00:00", Schedule{Calendar: "*-*-* 03:00:00"}.Describe())
}
