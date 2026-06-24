// Package logging builds icebeam's persistent slog logger. It writes structured
// records to a log file under the XDG state directory (overridable via config),
// optionally mirrors human-friendly output to stderr when attached to a TTY, and
// redacts secret values so credentials never reach the log.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/itspriddle/icebeam/internal/config"
)

// File permissions for the log file and its containing directory. The log can
// contain backup paths and host details, so we keep it owner-only to match the
// rest of icebeam's on-disk artifacts.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// logFileName is the basename of the default log file.
const logFileName = "icebeam.log"

// maxLogSize is the size threshold (10 MiB) at which the log file is rotated on
// open: the existing file is renamed to "<name>.1" (replacing any previous
// backup) and a fresh file is started. This is a simple, dependency-free guard
// against unbounded growth; OS-level logrotate may also be layered on top.
const maxLogSize = 10 * 1024 * 1024

// Logger is icebeam's logger. It wraps an *slog.Logger and owns the underlying
// log file so callers can release it when the process exits.
type Logger struct {
	*slog.Logger

	file io.Closer
	path string
}

// Path returns the resolved path of the log file the logger writes to.
func (l *Logger) Path() string { return l.path }

// Close releases the underlying log file. It is safe to call when no file was
// opened (e.g. a logger built solely against a custom writer).
func (l *Logger) Close() error {
	if l.file == nil {
		return nil
	}
	return l.file.Close()
}

// Options configure how New builds the logger.
type Options struct {
	// TTY reports whether stderr is attached to a terminal. When true, the
	// logger also mirrors human-friendly text records to stderr; when false
	// (e.g. under launchd/systemd) the file is the sole system of record.
	TTY bool

	// Stderr is the destination for the TTY mirror. Defaults to os.Stderr when
	// nil and TTY is true. Exposed primarily for tests.
	Stderr io.Writer
}

// New builds a Logger from the config's log settings. The log file path is
// taken from cfg.Log.File when set, otherwise the default under the XDG state
// directory. The level is parsed from cfg.Log.Level (defaulting to info). The
// file (0600) and its directory (0700) are created with restrictive perms, and
// the file is rotated if it has exceeded the size threshold.
func New(cfg *config.Config, opts Options) (*Logger, error) {
	path, err := ResolvePath(cfg)
	if err != nil {
		return nil, err
	}

	level, err := ParseLevel(cfg.Log.Level)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	if err := rotateIfTooLarge(path); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, fileMode) //nolint:gosec // path derived from XDG state dir or config, not arbitrary input
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}

	// OpenFile honors the mode only on creation; enforce perms on an existing
	// (possibly looser) file too.
	if err := os.Chmod(path, fileMode); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("chmod log %s: %w", path, err)
	}

	handler := newHandler(f, opts, level)

	return &Logger{
		Logger: slog.New(handler),
		file:   f,
		path:   path,
	}, nil
}

// NewWithWriter builds a Logger that writes to the supplied writer rather than a
// file. It performs no file or directory creation and Close is a no-op. This is
// primarily a testing seam.
func NewWithWriter(w io.Writer, level slog.Level, opts Options) *Logger {
	return &Logger{
		Logger: slog.New(newHandler(w, opts, level)),
	}
}

// newHandler builds the slog handler: a JSON handler writing structured records
// to primary, optionally fanned out to a human-friendly text handler on stderr
// when attached to a TTY. Secret values are redacted on both.
func newHandler(primary io.Writer, opts Options, level slog.Level) slog.Handler {
	handlerOpts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redactReplaceAttr,
	}

	fileHandler := slog.NewJSONHandler(primary, handlerOpts)

	if !opts.TTY {
		return fileHandler
	}

	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	return fanout{
		fileHandler,
		slog.NewTextHandler(stderr, handlerOpts),
	}
}

// ResolvePath returns the log file path: cfg.Log.File when non-empty, otherwise
// the default icebeam.log under the XDG state directory.
func ResolvePath(cfg *config.Config) (string, error) {
	if cfg != nil && cfg.Log.File != "" {
		return cfg.Log.File, nil
	}

	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, logFileName), nil
}

// ParseLevel maps a config log level string to an slog.Level. An empty string
// defaults to info; any other unrecognized value is an error naming the field.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("log.level: %q is not a valid level (use debug, info, warn, or error)", level)
	}
}

// rotateIfTooLarge renames an over-sized log file to "<name>.1" so a fresh file
// can be started. A missing file is fine (nothing to rotate).
func rotateIfTooLarge(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat log %s: %w", path, err)
	}

	if fi.Size() < maxLogSize {
		return nil
	}

	if err := os.Rename(path, path+".1"); err != nil {
		return fmt.Errorf("rotate log %s: %w", path, err)
	}

	return nil
}
