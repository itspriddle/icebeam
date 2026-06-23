package restic

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// versionRe extracts the dotted version number from restic's `version` output,
// e.g. "restic 0.16.0 compiled with go1.21 on darwin/arm64" → "0.16.0".
var versionRe = regexp.MustCompile(`restic\s+(\d+\.\d+(?:\.\d+)?)`)

// Version returns the restic binary's reported version string (e.g. "0.16.0").
func (r *Runner) Version(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, r.binary, "version") //nolint:gosec // binary resolved from config/PATH, "version" is a literal arg
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("restic: run `restic version`: %w", err)
	}

	m := versionRe.FindStringSubmatch(out.String())
	if m == nil {
		return "", fmt.Errorf("restic: could not parse version from %q", strings.TrimSpace(out.String()))
	}
	return m[1], nil
}

// ensureVersion verifies, at most once per Runner, that the restic binary meets
// the configured minimum version. A binary older than minVersion is a clear
// error. An empty minVersion skips the check.
func (r *Runner) ensureVersion(ctx context.Context) error {
	if r.checkedVersion || r.minVersion == "" {
		return nil
	}

	got, err := r.Version(ctx)
	if err != nil {
		return err
	}

	cmp, err := compareVersions(got, r.minVersion)
	if err != nil {
		return err
	}

	if cmp < 0 {
		return fmt.Errorf(
			"restic: version %s is older than the required minimum %s; please upgrade restic",
			got, r.minVersion,
		)
	}

	r.checkedVersion = true
	return nil
}

// compareVersions compares two dotted numeric versions (e.g. "0.16.0"). It
// returns -1 if a < b, 0 if equal, and +1 if a > b. Missing components are
// treated as zero so "0.16" compares as "0.16.0".
func compareVersions(a, b string) (int, error) {
	av, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseVersion(b)
	if err != nil {
		return 0, err
	}

	for i := range max(len(av), len(bv)) {
		var ac, bc int
		if i < len(av) {
			ac = av[i]
		}
		if i < len(bv) {
			bc = bv[i]
		}
		switch {
		case ac < bc:
			return -1, nil
		case ac > bc:
			return 1, nil
		}
	}
	return 0, nil
}

// parseVersion splits a dotted numeric version into its integer components,
// tolerating a leading "v" and ignoring any pre-release/build suffix.
func parseVersion(v string) ([]int, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop any pre-release or build metadata (e.g. "0.16.0-rc1").
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}

	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("restic: invalid version %q: %w", v, err)
		}
		out[i] = n
	}
	return out, nil
}
