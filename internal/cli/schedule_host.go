package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// scheduleUnitFileMode/dirMode keep the generated scheduler units owner-only,
// matching icebeam's config/secret permission convention.
const (
	scheduleUnitFileMode = 0o600
	scheduleUnitDirMode  = 0o700
)

// xdgBase resolves an XDG base directory: it honors envVar when set to an
// absolute path (mirroring the config package), otherwise returns the given
// default. Unlike config.ConfigDir/StateDir it returns the *base* (e.g.
// ~/.config), not icebeam's namespaced subdir, since that is what the scheduler
// unit pins as $XDG_CONFIG_HOME/$XDG_STATE_HOME.
func xdgBase(envVar, def string) (string, error) {
	if base := os.Getenv(envVar); filepath.IsAbs(base) {
		return base, nil
	}
	return def, nil
}

// resolveBinaryPath returns the absolute path to the running icebeam binary so
// the generated unit invokes the same binary the user installed from.
func resolveBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve icebeam binary path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// Fall back to the unresolved path rather than failing the install.
		return exe, nil
	}
	return resolved, nil
}

// writeUnitFile writes a generated scheduler unit to path with owner-only perms,
// creating the containing directory (0700) first.
func writeUnitFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), scheduleUnitDirMode); err != nil {
		return fmt.Errorf("create unit dir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), scheduleUnitFileMode); err != nil {
		return fmt.Errorf("write unit %s: %w", path, err)
	}
	return nil
}

// fileExists reports whether path exists (any error reading it counts as absent).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// runScheduleCommand runs a scheduler activation command (launchctl/systemctl),
// returning its combined output. A missing binary surfaces as
// errSchedulerUnsupported so callers fall back to manual-install guidance.
func runScheduleCommand(ctx context.Context, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", errSchedulerUnsupported
	}

	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is a fixed scheduler binary (launchctl/systemctl), not user input
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}
