package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/itspriddle/icebeam/internal/schedule"
)

// errSchedulerUnsupported signals the host has no scheduler icebeam can drive
// (e.g. a Synology without `systemctl --user`). Commands turn it into guidance to
// use `--print` and install manually rather than a hard failure.
var errSchedulerUnsupported = errors.New("no supported OS scheduler is available")

// scheduleEnv is the host environment the schedule commands act against. The
// fields are package-injectable so tests can drive install/uninstall/status
// against a temp home without touching the real launchd/systemd or filesystem.
type scheduleEnv struct {
	// goos is the target platform (runtime.GOOS).
	goos string
	// home is the user's home directory (for the LaunchAgents path).
	home string
	// configHome is the XDG config base ($XDG_CONFIG_HOME or ~/.config).
	configHome string
	// stateHome is the XDG state base ($XDG_STATE_HOME or ~/.local/state).
	stateHome string
	// binaryPath is the resolved icebeam binary path the unit invokes.
	binaryPath string
	// runActivation runs a scheduler activation command (launchctl/systemctl),
	// returning combined output. A nil func or an unavailable scheduler makes
	// install/uninstall write/remove the unit files but skip activation.
	runActivation func(ctx context.Context, name string, args ...string) (string, error)
}

// newScheduleEnv resolves the real host environment. It is a package var so tests
// can swap in a hermetic one.
var newScheduleEnv = func() (*scheduleEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	configHome := xdgBase("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	stateHome := xdgBase("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))

	bin, err := resolveBinaryPath()
	if err != nil {
		return nil, err
	}

	return &scheduleEnv{
		goos:          runtime.GOOS,
		home:          home,
		configHome:    configHome,
		stateHome:     stateHome,
		binaryPath:    bin,
		runActivation: runScheduleCommand,
	}, nil
}

// scheduleFlags collects the flags shared by `schedule install`.
type scheduleFlags struct {
	interval string
	calendar string
	print    bool
}

// newScheduleCommand builds the `icebeam schedule` command group.
func newScheduleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Install, uninstall, or inspect the OS scheduler unit",
		Long: "schedule generates and installs the OS-native scheduler unit that runs " +
			"`icebeam run` on a recurring schedule: a launchd LaunchAgent on macOS or a " +
			"systemd service+timer on Linux. Use `schedule install --print` to emit the " +
			"unit without installing it (for locked-down systems or manual setup).",
	}

	cmd.AddCommand(newScheduleInstallCommand())
	cmd.AddCommand(newScheduleUninstallCommand())
	cmd.AddCommand(newScheduleStatusCommand())

	return cmd
}

// newScheduleInstallCommand builds `icebeam schedule install`.
func newScheduleInstallCommand() *cobra.Command {
	flags := &scheduleFlags{}

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the OS scheduler unit for `icebeam run`",
		Long: "install generates and installs the scheduler unit (launchd agent on " +
			"macOS, systemd service+timer on Linux) that runs `icebeam run` on the " +
			"configured interval. Use --print to emit the unit to stdout without " +
			"installing it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScheduleInstall(cmd, flags)
		},
	}

	f := cmd.Flags()
	f.StringVar(&flags.interval, "interval", "", "recurrence shorthand: hourly, daily, or weekly (default daily)")
	f.StringVar(&flags.calendar, "calendar", "", "raw systemd OnCalendar expression (Linux only; mutually exclusive with --interval)")
	f.BoolVar(&flags.print, "print", false, "print the generated unit to stdout without installing it")
	cmd.MarkFlagsMutuallyExclusive("interval", "calendar")

	return cmd
}

// newScheduleUninstallCommand builds `icebeam schedule uninstall`.
func newScheduleUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the installed scheduler unit",
		Long:  "uninstall removes the scheduler unit icebeam installed and deactivates it.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScheduleUninstall(cmd)
		},
	}
}

// newScheduleStatusCommand builds `icebeam schedule status`.
func newScheduleStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether the scheduler unit is installed",
		Long: "status reports whether icebeam's scheduler unit is installed and, where " +
			"the platform exposes it, its next and last run.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScheduleStatus(cmd)
		},
	}
}

// runScheduleInstall builds the spec and installs (or prints) the unit for the
// host platform.
func runScheduleInstall(cmd *cobra.Command, flags *scheduleFlags) error {
	sched, err := schedule.NewSchedule(flags.interval, flags.calendar)
	if err != nil {
		return err
	}

	env, err := newScheduleEnv()
	if err != nil {
		return err
	}

	spec := scheduleSpec(env, sched)

	switch env.goos {
	case "darwin":
		return installLaunchd(cmd, env, spec, flags.print)
	case "linux":
		return installSystemd(cmd, env, spec, flags.print)
	default:
		return unsupportedScheduler(cmd, env.goos)
	}
}

// runScheduleUninstall removes the installed unit for the host platform.
func runScheduleUninstall(cmd *cobra.Command) error {
	env, err := newScheduleEnv()
	if err != nil {
		return err
	}

	switch env.goos {
	case "darwin":
		return uninstallLaunchd(cmd, env)
	case "linux":
		return uninstallSystemd(cmd, env)
	default:
		return unsupportedScheduler(cmd, env.goos)
	}
}

// runScheduleStatus reports the install state for the host platform.
func runScheduleStatus(cmd *cobra.Command) error {
	env, err := newScheduleEnv()
	if err != nil {
		return err
	}

	switch env.goos {
	case "darwin":
		return statusLaunchd(cmd, env)
	case "linux":
		return statusSystemd(cmd, env)
	default:
		return unsupportedScheduler(cmd, env.goos)
	}
}

// scheduleSpec assembles the templating spec from the host environment and the
// resolved schedule.
func scheduleSpec(env *scheduleEnv, sched schedule.Schedule) schedule.Spec {
	return schedule.Spec{
		BinaryPath:    env.binaryPath,
		XDGConfigHome: env.configHome,
		XDGStateHome:  env.stateHome,
		LogPath:       defaultLogPath(env.stateHome),
		Schedule:      sched,
	}
}

// installLaunchd writes the LaunchAgent plist and loads it via launchctl. With
// print it only emits the plist.
func installLaunchd(cmd *cobra.Command, env *scheduleEnv, spec schedule.Spec, printOnly bool) error {
	plist, err := schedule.GenerateLaunchd(spec)
	if err != nil {
		return err
	}

	if printOnly {
		writeLine(cmd.OutOrStdout(), plist)
		return nil
	}

	path := schedule.LaunchdPlistPath(env.home)
	if err := writeUnitFile(path, plist); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	writeLine(out, fmt.Sprintf("Installed launchd agent at %s", path))

	// Reload so an existing agent picks up the new definition.
	_, _ = env.activate(cmd.Context(), "launchctl", "unload", path)
	if activateOut, err := env.activate(cmd.Context(), "launchctl", "load", path); err != nil {
		writeLine(out, fmt.Sprintf("Wrote the plist but could not load it via launchctl: %v", err))
		if activateOut != "" {
			writeLine(out, activateOut)
		}
		writeLine(out, fmt.Sprintf("Load it manually with: launchctl load %s", path))
		return nil
	}

	writeLine(out, fmt.Sprintf("Scheduled `icebeam run` %s.", spec.Schedule.Describe()))
	return nil
}

// installSystemd writes the service+timer units and enables the timer via
// systemctl --user. With print it emits both units.
func installSystemd(cmd *cobra.Command, env *scheduleEnv, spec schedule.Spec, printOnly bool) error {
	service, timer, err := schedule.GenerateSystemd(spec)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if printOnly {
		writeLine(out, "# "+schedule.ServiceUnitName())
		writeLine(out, service)
		writeLine(out, "# "+schedule.TimerUnitName())
		writeLine(out, timer)
		return nil
	}

	servicePath := schedule.SystemdServicePath(env.configHome)
	timerPath := schedule.SystemdTimerPath(env.configHome)
	if err := writeUnitFile(servicePath, service); err != nil {
		return err
	}
	if err := writeUnitFile(timerPath, timer); err != nil {
		return err
	}

	writeLine(out, fmt.Sprintf("Installed systemd units at %s and %s", servicePath, timerPath))

	if _, err := env.activate(cmd.Context(), "systemctl", "--user", "daemon-reload"); err != nil {
		return systemdManualHint(out, err, spec)
	}
	if activateOut, err := env.activate(cmd.Context(), "systemctl", "--user", "enable", "--now", schedule.TimerUnitName()); err != nil {
		if activateOut != "" {
			writeLine(out, activateOut)
		}
		return systemdManualHint(out, err, spec)
	}

	writeLine(out, fmt.Sprintf("Scheduled `icebeam run` %s.", spec.Schedule.Describe()))
	return nil
}

// systemdManualHint reports that the units were written but could not be
// activated (e.g. no `systemctl --user`), pointing the user at manual steps.
func systemdManualHint(out io.Writer, cause error, spec schedule.Spec) error {
	writeLine(out, fmt.Sprintf("Wrote the units but could not activate them via systemctl --user: %v", cause))
	writeLine(out, fmt.Sprintf("Enable them manually with: systemctl --user enable --now %s", schedule.TimerUnitName()))
	writeLine(out, fmt.Sprintf("(scheduled interval: %s)", spec.Schedule.Describe()))
	return nil
}

// uninstallLaunchd unloads and removes the LaunchAgent plist.
func uninstallLaunchd(cmd *cobra.Command, env *scheduleEnv) error {
	path := schedule.LaunchdPlistPath(env.home)
	out := cmd.OutOrStdout()

	if !fileExists(path) {
		writeLine(out, "No launchd agent is installed.")
		return nil
	}

	_, _ = env.activate(cmd.Context(), "launchctl", "unload", path)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove launchd agent %s: %w", path, err)
	}

	writeLine(out, fmt.Sprintf("Removed launchd agent %s", path))
	return nil
}

// uninstallSystemd disables the timer and removes the service+timer units.
func uninstallSystemd(cmd *cobra.Command, env *scheduleEnv) error {
	servicePath := schedule.SystemdServicePath(env.configHome)
	timerPath := schedule.SystemdTimerPath(env.configHome)
	out := cmd.OutOrStdout()

	if !fileExists(servicePath) && !fileExists(timerPath) {
		writeLine(out, "No systemd units are installed.")
		return nil
	}

	_, _ = env.activate(cmd.Context(), "systemctl", "--user", "disable", "--now", schedule.TimerUnitName())

	var removed []string
	for _, path := range []string{timerPath, servicePath} {
		if !fileExists(path) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove systemd unit %s: %w", path, err)
		}
		removed = append(removed, path)
	}

	_, _ = env.activate(cmd.Context(), "systemctl", "--user", "daemon-reload")

	for _, path := range removed {
		writeLine(out, fmt.Sprintf("Removed systemd unit %s", path))
	}
	return nil
}

// statusLaunchd reports whether the LaunchAgent plist is installed.
func statusLaunchd(cmd *cobra.Command, env *scheduleEnv) error {
	path := schedule.LaunchdPlistPath(env.home)
	out := cmd.OutOrStdout()

	if !fileExists(path) {
		writeLine(out, "Not scheduled: no launchd agent is installed.")
		return nil
	}

	writeLine(out, fmt.Sprintf("Scheduled: launchd agent installed at %s", path))
	if info, err := env.activate(cmd.Context(), "launchctl", "list", schedule.Label); err == nil && info != "" {
		writeLine(out, info)
	}
	return nil
}

// statusSystemd reports whether the systemd timer is installed.
func statusSystemd(cmd *cobra.Command, env *scheduleEnv) error {
	timerPath := schedule.SystemdTimerPath(env.configHome)
	out := cmd.OutOrStdout()

	if !fileExists(timerPath) {
		writeLine(out, "Not scheduled: no systemd timer is installed.")
		return nil
	}

	writeLine(out, fmt.Sprintf("Scheduled: systemd timer installed at %s", timerPath))
	if info, err := env.activate(cmd.Context(), "systemctl", "--user", "list-timers", schedule.TimerUnitName()); err == nil && info != "" {
		writeLine(out, info)
	}
	return nil
}

// unsupportedScheduler reports a platform icebeam can't auto-schedule on and
// points the user at `--print` for manual installation.
func unsupportedScheduler(cmd *cobra.Command, goos string) error {
	out := cmd.OutOrStdout()
	writeLine(out, fmt.Sprintf("Automatic scheduling is not supported on %s.", goos))
	writeLine(out, "Use `icebeam schedule install --print` to emit a unit you can install by hand,")
	writeLine(out, "or set up a recurring task that runs `icebeam run` with your platform's own scheduler.")
	return nil
}

// activate runs a scheduler activation command if one is configured, returning
// errSchedulerUnsupported when none is (so callers fall back to manual guidance).
func (e *scheduleEnv) activate(ctx context.Context, name string, args ...string) (string, error) {
	if e.runActivation == nil {
		return "", errSchedulerUnsupported
	}
	return e.runActivation(ctx, name, args...)
}

// defaultLogPath returns icebeam's default log path under the given XDG state
// base. It mirrors the logging package's default layout.
func defaultLogPath(stateHome string) string {
	if stateHome == "" {
		return ""
	}
	return filepath.Join(stateHome, "icebeam", "icebeam.log")
}
