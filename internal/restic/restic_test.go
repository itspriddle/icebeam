package restic

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
)

// writeStub writes an executable shell script acting as a fake restic and
// returns its path. The script body is supplied by the caller so each test can
// shape restic's behavior (version output, JSON, exit code, env capture, etc.).
func writeStub(t *testing.T, body string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("stub restic relies on a POSIX shell")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "restic")
	script := "#!/bin/sh\n" + body
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755)) //nolint:gosec // test stub must be executable

	return path
}

// newRunner builds a Runner pointed at a stub binary with the given config
// tweaks applied. The credential store is a file-backed store in a temp dir with
// a known repo password set.
func newRunner(t *testing.T, binary string, mutate func(*config.Config)) *Runner {
	t.Helper()

	cfg := config.Default()
	cfg.Repository.URL = "rest:http://nas.local:8000/icebeam"
	cfg.Restic.Binary = binary
	cfg.Restic.MinVersion = "" // disable version gating unless a test opts in
	if mutate != nil {
		mutate(&cfg)
	}

	store, err := credentials.Open(credentials.BackendFile, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Set(credentials.RepoPassword, "s3cr3t"))

	r, err := New(&cfg, store, nil)
	require.NoError(t, err)
	return r
}

func TestNewResolvesBinaryFromConfig(t *testing.T) {
	t.Parallel()

	stub := writeStub(t, "exit 0\n")
	r := newRunner(t, stub, nil)
	assert.Equal(t, stub, r.Binary())
}

func TestNewMissingBinaryIsActionable(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.Repository.URL = "rest:http://nas.local:8000/icebeam"
	cfg.Restic.Binary = "/nonexistent/path/to/restic-xyz"

	_, err := New(&cfg, nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBinaryNotFound)
	assert.Contains(t, err.Error(), "install restic")
}

func TestNewFindsBinaryOnPath(t *testing.T) {
	// No t.Parallel(): t.Setenv (PATH) is incompatible with parallel tests.
	stub := writeStub(t, "exit 0\n")
	// Put the stub on PATH under the name "restic" and leave config.Restic.Binary
	// empty so it is discovered via PATH.
	dir := filepath.Dir(stub)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config.Default()
	cfg.Repository.URL = "rest:http://nas.local:8000/icebeam"
	cfg.Restic.Binary = ""

	store, err := credentials.Open(credentials.BackendFile, t.TempDir())
	require.NoError(t, err)

	r, err := New(&cfg, store, nil)
	require.NoError(t, err)
	assert.Equal(t, stub, r.Binary())
}

func TestRunConstructsEnvWithoutSecretsInArgv(t *testing.T) {
	t.Parallel()

	// The stub dumps its argv and the relevant env vars to files we inspect.
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	envFile := filepath.Join(dir, "env")
	stub := writeStub(t, `
printf '%s\n' "$@" > `+argvFile+`
env | grep -E '^RESTIC_' > `+envFile+`
exit 0
`)

	r := newRunner(t, stub, nil)

	require.NoError(t, r.Run(context.Background(), "snapshots", "--tag", "home"))

	argv, err := os.ReadFile(argvFile) //nolint:gosec // test temp-dir path, not arbitrary input
	require.NoError(t, err)
	assert.Equal(t, "snapshots\n--tag\nhome\n", string(argv))
	assert.NotContains(t, string(argv), "s3cr3t", "the password must never appear in argv")
	assert.NotContains(t, string(argv), "RESTIC_PASSWORD")

	envOut, err := os.ReadFile(envFile) //nolint:gosec // test temp-dir path, not arbitrary input
	require.NoError(t, err)
	assert.Contains(t, string(envOut), "RESTIC_REPOSITORY=rest:http://nas.local:8000/icebeam")
	// File backend hands restic the password file, not the password itself.
	assert.Contains(t, string(envOut), "RESTIC_PASSWORD_FILE=")
	assert.NotContains(t, string(envOut), "s3cr3t")
}

func TestRunSurfacesRESTServerCredentialsInEnv(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	stub := writeStub(t, "env | grep -E '^RESTIC_REST_' > "+envFile+"\nexit 0\n")

	fileDir := t.TempDir()
	store, err := credentials.Open(credentials.BackendFile, fileDir)
	require.NoError(t, err)
	require.NoError(t, store.Set(credentials.RepoPassword, "s3cr3t"))
	require.NoError(t, store.Set(credentials.RESTUsername, "nasuser"))
	require.NoError(t, store.Set(credentials.RESTPassword, "naspass"))

	cfg := config.Default()
	cfg.Repository.URL = "rest:http://nas.local:8000/icebeam"
	cfg.Restic.Binary = stub
	cfg.Restic.MinVersion = ""

	r, err := New(&cfg, store, nil)
	require.NoError(t, err)

	require.NoError(t, r.Run(context.Background(), "snapshots"))

	envOut, err := os.ReadFile(envFile) //nolint:gosec // test temp-dir path, not arbitrary input
	require.NoError(t, err)
	assert.Contains(t, string(envOut), "RESTIC_REST_USERNAME=nasuser")
	assert.Contains(t, string(envOut), "RESTIC_REST_PASSWORD=naspass")
}

func TestRunOmitsRESTCredentialsWhenAbsent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	stub := writeStub(t, "env | grep -E '^RESTIC_REST_' > "+envFile+" || true\nexit 0\n")

	r := newRunner(t, stub, nil)
	require.NoError(t, r.Run(context.Background(), "snapshots"))

	envOut, err := os.ReadFile(envFile) //nolint:gosec // test temp-dir path, not arbitrary input
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(envOut)), "no REST env when no REST credentials are stored")
}

func TestRunStreamsCombinedOutputToLogger(t *testing.T) {
	t.Parallel()

	// The stub writes to both stdout and stderr; both must reach the logger.
	stub := writeStub(t, "echo to-stdout\necho to-stderr >&2\nexit 0\n")

	var buf bytes.Buffer
	logger := logging.NewWithWriter(&buf, slog.LevelDebug, logging.Options{})

	cfg := config.Default()
	cfg.Repository.URL = "rest:http://nas.local:8000/icebeam"
	cfg.Restic.Binary = stub
	cfg.Restic.MinVersion = ""

	store, err := credentials.Open(credentials.BackendFile, t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Set(credentials.RepoPassword, "s3cr3t"))

	r, err := New(&cfg, store, logger)
	require.NoError(t, err)

	require.NoError(t, r.Run(context.Background(), "backup"))

	out := buf.String()
	assert.Contains(t, out, "to-stdout")
	assert.Contains(t, out, "to-stderr")
	assert.NotContains(t, out, "s3cr3t", "no secret should ever reach the log")
}

func TestVersionParsesResticOutput(t *testing.T) {
	t.Parallel()

	stub := writeStub(t, `echo "restic 0.16.2 compiled with go1.21 on darwin/arm64"`+"\n")
	r := newRunner(t, stub, nil)

	v, err := r.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "0.16.2", v)
}

func TestVersionGatingRejectsTooOld(t *testing.T) {
	t.Parallel()

	stub := writeStub(t, `echo "restic 0.15.0 compiled with go1.20 on linux/amd64"`+"\n")
	r := newRunner(t, stub, func(c *config.Config) { c.Restic.MinVersion = "0.16.0" })

	err := r.Run(context.Background(), "snapshots")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "older than")
	assert.Contains(t, err.Error(), "0.16.0")
}

func TestVersionGatingAcceptsAtLeastMinimum(t *testing.T) {
	t.Parallel()

	// Equal and newer must both pass.
	for _, ver := range []string{"0.16.0", "0.16.5", "0.17.0", "1.0.0"} {
		stub := writeStub(t, "echo \"restic "+ver+" compiled with go1.21 on linux/amd64\"\nexit 0\n")
		r := newRunner(t, stub, func(c *config.Config) { c.Restic.MinVersion = "0.16.0" })
		assert.NoError(t, r.Run(context.Background(), "snapshots"), "version %s should pass gating", ver)
	}
}

func TestRunParsesJSON(t *testing.T) {
	t.Parallel()

	// The stub asserts --json is the first argument, then emits a JSON document
	// on stdout (progress goes to stderr in real restic; the stub skips that).
	stub := writeStub(t, `
[ "$1" = "--json" ] || { echo "missing --json" >&2; exit 2; }
echo '[{"id":"abc123","hostname":"nas","tags":["home"]}]'
`)
	r := newRunner(t, stub, nil)

	var snapshots []struct {
		ID       string   `json:"id"`
		Hostname string   `json:"hostname"`
		Tags     []string `json:"tags"`
	}
	require.NoError(t, r.RunJSON(context.Background(), &snapshots, "snapshots"))
	require.Len(t, snapshots, 1)
	assert.Equal(t, "abc123", snapshots[0].ID)
	assert.Equal(t, "nas", snapshots[0].Hostname)
	assert.Equal(t, []string{"home"}, snapshots[0].Tags)
}

func TestRunJSONRejectsMalformedOutput(t *testing.T) {
	t.Parallel()

	stub := writeStub(t, "echo 'not json'\n")
	r := newRunner(t, stub, nil)

	var out []any
	err := r.RunJSON(context.Background(), &out, "snapshots")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestRunMapsExitCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		code   int
		assert func(t *testing.T, e *ExitError)
	}{
		{"locked", ExitRepoLocked, func(t *testing.T, e *ExitError) {
			assert.True(t, e.IsRepoLocked())
			assert.False(t, e.IsWrongPassword())
		}},
		{"no-repo", ExitRepoNotExist, func(t *testing.T, e *ExitError) {
			assert.True(t, e.IsRepoNotExist())
		}},
		{"wrong-password", ExitWrongPassword, func(t *testing.T, e *ExitError) {
			assert.True(t, e.IsWrongPassword())
		}},
		{"incomplete-backup", ExitIncompleteBackup, func(t *testing.T, e *ExitError) {
			assert.True(t, e.IsIncompleteBackup())
		}},
		{"generic", ExitGeneric, func(t *testing.T, e *ExitError) {
			assert.False(t, e.IsRepoLocked())
			assert.False(t, e.IsRepoNotExist())
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := writeStub(t, "exit "+strconv.Itoa(tc.code)+"\n")
			r := newRunner(t, stub, nil)

			err := r.Run(context.Background(), "backup")
			require.Error(t, err)

			var exitErr *ExitError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, tc.code, exitErr.Code)
			assert.Equal(t, "backup", exitErr.Command)
			tc.assert(t, exitErr)
		})
	}
}

func TestVersionMissingBinaryRunError(t *testing.T) {
	t.Parallel()

	// A stub that fails before printing anything parseable.
	stub := writeStub(t, "exit 3\n")
	r := newRunner(t, stub, nil)

	_, err := r.Version(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}
