package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/schedule"
)

// activationStub records the scheduler activation commands the schedule commands
// run and replays a scripted result, so tests drive install/uninstall/status
// without a real launchctl/systemctl.
type activationStub struct {
	calls [][]string
	out   string
	err   error
}

func (s *activationStub) run(_ context.Context, name string, args ...string) (string, error) {
	s.calls = append(s.calls, append([]string{name}, args...))
	return s.out, s.err
}

// withScheduleEnv swaps newScheduleEnv for one returning a hermetic env rooted at
// a temp home, with the given goos and activation hook. It returns the env so
// tests can read the resolved paths. The activation hook may be nil to simulate a
// host with no scheduler (e.g. Synology without systemctl --user).
func withScheduleEnv(t *testing.T, goos string, activation func(context.Context, string, ...string) (string, error)) *scheduleEnv {
	t.Helper()

	home := t.TempDir()
	env := &scheduleEnv{
		goos:          goos,
		home:          home,
		configHome:    filepath.Join(home, ".config"),
		stateHome:     filepath.Join(home, ".local", "state"),
		binaryPath:    "/usr/local/bin/icebeam",
		runActivation: activation,
	}

	orig := newScheduleEnv
	newScheduleEnv = func() (*scheduleEnv, error) { return env, nil }
	t.Cleanup(func() { newScheduleEnv = orig })

	return env
}

func TestScheduleInstallLaunchd(t *testing.T) {
	act := &activationStub{}
	env := withScheduleEnv(t, "darwin", act.run)

	out, err := runCLI(t, "schedule", "install", "--interval", "daily")
	require.NoError(t, err)

	path := schedule.LaunchdPlistPath(env.home)
	require.FileExists(t, path)

	data, err := os.ReadFile(path) //nolint:gosec // test temp-dir path
	require.NoError(t, err)
	plist := string(data)
	assert.Contains(t, plist, "<key>StartCalendarInterval</key>")
	assert.Contains(t, plist, "<string>/usr/local/bin/icebeam</string>")

	// Owner-only perms on the unit file.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// launchctl load was invoked.
	var loaded bool
	for _, c := range act.calls {
		if c[0] == "launchctl" && contains(c, "load") {
			loaded = true
		}
	}
	assert.True(t, loaded, "expected a launchctl load call, got %v", act.calls)
	assert.Contains(t, out, "Installed launchd agent")
}

func TestScheduleInstallSystemd(t *testing.T) {
	act := &activationStub{}
	env := withScheduleEnv(t, "linux", act.run)

	out, err := runCLI(t, "schedule", "install", "--interval", "hourly")
	require.NoError(t, err)

	servicePath := schedule.SystemdServicePath(env.configHome)
	timerPath := schedule.SystemdTimerPath(env.configHome)
	require.FileExists(t, servicePath)
	require.FileExists(t, timerPath)

	timer, err := os.ReadFile(timerPath) //nolint:gosec // test temp-dir path
	require.NoError(t, err)
	assert.Contains(t, string(timer), "OnCalendar=hourly")

	// systemctl --user enable --now was invoked for the timer.
	var enabled bool
	for _, c := range act.calls {
		if c[0] == "systemctl" && contains(c, "enable") {
			enabled = true
		}
	}
	assert.True(t, enabled, "expected a systemctl enable call, got %v", act.calls)
	assert.Contains(t, out, "Installed systemd units")
}

func TestScheduleInstallRawCalendarSystemd(t *testing.T) {
	act := &activationStub{}
	env := withScheduleEnv(t, "linux", act.run)

	_, err := runCLI(t, "schedule", "install", "--calendar", "*-*-* 03:00:00")
	require.NoError(t, err)

	timer, err := os.ReadFile(schedule.SystemdTimerPath(env.configHome)) //nolint:gosec // test temp-dir path
	require.NoError(t, err)
	assert.Contains(t, string(timer), "OnCalendar=*-*-* 03:00:00")
}

func TestScheduleInstallPrintDoesNotInstall(t *testing.T) {
	act := &activationStub{}
	env := withScheduleEnv(t, "darwin", act.run)

	out, err := runCLI(t, "schedule", "install", "--print")
	require.NoError(t, err)

	// --print emits the unit but writes nothing and activates nothing.
	assert.NoFileExists(t, schedule.LaunchdPlistPath(env.home))
	assert.Empty(t, act.calls)
	assert.Contains(t, out, "<?xml")
	assert.Contains(t, out, "<key>Label</key>")
}

func TestScheduleInstallPrintSystemd(t *testing.T) {
	act := &activationStub{}
	env := withScheduleEnv(t, "linux", act.run)

	out, err := runCLI(t, "schedule", "install", "--print")
	require.NoError(t, err)

	assert.NoFileExists(t, schedule.SystemdServicePath(env.configHome))
	assert.Empty(t, act.calls)
	assert.Contains(t, out, "Type=oneshot")
	assert.Contains(t, out, "OnCalendar=")
}

func TestScheduleInstallGracefulWithoutScheduler(t *testing.T) {
	// No activation hook simulates a host without launchctl/systemctl (Synology).
	env := withScheduleEnv(t, "linux", nil)

	out, err := runCLI(t, "schedule", "install")
	require.NoError(t, err)

	// The unit files are still written so a manual install is possible.
	require.FileExists(t, schedule.SystemdTimerPath(env.configHome))
	// And the user is told how to activate them by hand.
	assert.Contains(t, out, "could not activate")
	assert.Contains(t, out, "systemctl --user enable --now")
}

func TestScheduleInstallUnsupportedPlatform(t *testing.T) {
	withScheduleEnv(t, "plan9", nil)

	out, err := runCLI(t, "schedule", "install")
	require.NoError(t, err)
	assert.Contains(t, out, "not supported on plan9")
	assert.Contains(t, out, "--print")
}

func TestScheduleInstallRejectsIntervalAndCalendar(t *testing.T) {
	withScheduleEnv(t, "linux", (&activationStub{}).run)

	_, err := runCLI(t, "schedule", "install", "--interval", "daily", "--calendar", "*-*-* 03:00:00")
	require.Error(t, err)
}

func TestScheduleUninstallLaunchd(t *testing.T) {
	act := &activationStub{}
	env := withScheduleEnv(t, "darwin", act.run)

	// Install first.
	_, err := runCLI(t, "schedule", "install")
	require.NoError(t, err)
	require.FileExists(t, schedule.LaunchdPlistPath(env.home))

	out, err := runCLI(t, "schedule", "uninstall")
	require.NoError(t, err)

	assert.NoFileExists(t, schedule.LaunchdPlistPath(env.home))
	assert.Contains(t, out, "Removed launchd agent")
}

func TestScheduleUninstallSystemd(t *testing.T) {
	act := &activationStub{}
	env := withScheduleEnv(t, "linux", act.run)

	_, err := runCLI(t, "schedule", "install")
	require.NoError(t, err)
	require.FileExists(t, schedule.SystemdTimerPath(env.configHome))

	out, err := runCLI(t, "schedule", "uninstall")
	require.NoError(t, err)

	assert.NoFileExists(t, schedule.SystemdServicePath(env.configHome))
	assert.NoFileExists(t, schedule.SystemdTimerPath(env.configHome))
	assert.Contains(t, out, "Removed systemd unit")

	// systemctl disable --now was invoked.
	var disabled bool
	for _, c := range act.calls {
		if c[0] == "systemctl" && contains(c, "disable") {
			disabled = true
		}
	}
	assert.True(t, disabled, "expected a systemctl disable call, got %v", act.calls)
}

func TestScheduleUninstallWhenNotInstalled(t *testing.T) {
	act := &activationStub{}
	withScheduleEnv(t, "darwin", act.run)

	out, err := runCLI(t, "schedule", "uninstall")
	require.NoError(t, err)
	assert.Contains(t, out, "No launchd agent is installed")
	assert.Empty(t, act.calls)
}

func TestScheduleStatusNotInstalled(t *testing.T) {
	withScheduleEnv(t, "linux", (&activationStub{}).run)

	out, err := runCLI(t, "schedule", "status")
	require.NoError(t, err)
	assert.Contains(t, out, "Not scheduled")
}

func TestScheduleStatusInstalled(t *testing.T) {
	act := &activationStub{out: "next run: tomorrow"}
	env := withScheduleEnv(t, "linux", act.run)

	_, err := runCLI(t, "schedule", "install")
	require.NoError(t, err)

	out, err := runCLI(t, "schedule", "status")
	require.NoError(t, err)
	assert.Contains(t, out, "Scheduled")
	assert.Contains(t, out, schedule.SystemdTimerPath(env.configHome))
	// The platform's run info is surfaced when available.
	assert.Contains(t, out, "next run")
}

// contains reports whether want appears in args.
func contains(args []string, want string) bool {
	for _, a := range args {
		if strings.Contains(a, want) {
			return true
		}
	}
	return false
}
