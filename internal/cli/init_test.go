package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/credentials"
	"github.com/itspriddle/icebeam/internal/logging"
	"github.com/itspriddle/icebeam/internal/restic"
)

// stubRunner records the restic invocations init makes and replays scripted
// errors so tests can exercise the probe/init flow without a real restic.
type stubRunner struct {
	calls   [][]string
	results map[string]error // keyed by the first arg (subcommand)
	// catQueue, when non-nil, supplies one result per `cat config` probe in
	// order (used to drive the wrong-password-then-correct retry loop). It takes
	// precedence over results["cat"]; once drained, the last entry is reused.
	catQueue []error
}

func (s *stubRunner) Run(_ context.Context, args ...string) error {
	s.calls = append(s.calls, args)
	if len(args) == 0 {
		return nil
	}
	if args[0] == "cat" && s.catQueue != nil {
		err := s.catQueue[0]
		if len(s.catQueue) > 1 {
			s.catQueue = s.catQueue[1:]
		}
		return err
	}
	return s.results[args[0]]
}

// withStubRunner swaps newResticRunner for one returning the given stub and
// restores it when the test ends. It also returns the credential store the init
// command opened so assertions can read it back.
func withStubRunner(t *testing.T, stub *stubRunner) {
	t.Helper()
	orig := newResticRunner
	newResticRunner = func(*config.Config, credentials.CredentialStore, *logging.Logger) (resticRunner, error) {
		return stub, nil
	}
	t.Cleanup(func() { newResticRunner = orig })
}

// isolateXDG points config/state/credentials at a temp dir so init never touches
// the real machine. It returns the temp dir (the XDG base).
func isolateXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	return dir
}

// runInitCmd drives the init command with the given args and a stdin string,
// returning combined output and the execution error.
func runInitCmd(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()

	root := NewRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"init"}, args...))

	err := root.Execute()
	return out.String(), err
}

func TestInitNonInteractiveInitializesAbsentRepo(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{
		results: map[string]error{
			// `cat config` on a fresh repo reports repo-not-exist (code 10).
			"cat": &restic.ExitError{Code: restic.ExitRepoNotExist, Command: "cat"},
		},
	}
	withStubRunner(t, stub)

	out, err := runInitCmd(t, "hunter2\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home",
		"--path", "/Users/josh/Documents",
		"--path", "/Users/josh/Projects",
		"--exclude", "**/node_modules",
		"--tag", "home",
		"--backend", "file",
		"--password-stdin",
	)
	require.NoError(t, err)

	// Probe then init were invoked, in order.
	require.Len(t, stub.calls, 2)
	assert.Equal(t, []string{"cat", "config"}, stub.calls[0])
	assert.Equal(t, []string{"init"}, stub.calls[1])

	// Config was written and round-trips with the supplied values.
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/icebeam", cfg.Repository.URL)
	assert.Equal(t, "file", cfg.Credentials.Backend)
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "home", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/Users/josh/Documents", "/Users/josh/Projects"}, cfg.Sets[0].Paths)
	assert.Equal(t, []string{"**/node_modules"}, cfg.Sets[0].Exclude)
	assert.Equal(t, []string{"home"}, cfg.Sets[0].Tags)

	// Secret was stored via the file backend and never appears in any restic argv.
	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "hunter2", got)
	for _, call := range stub.calls {
		assert.NotContains(t, strings.Join(call, " "), "hunter2")
	}

	assert.Contains(t, out, "Initializing")
	assert.Contains(t, out, "icebeam run")
}

func TestInitLogsStartAndEndThroughTheLogger(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{
		results: map[string]error{
			"cat": &restic.ExitError{Code: restic.ExitRepoNotExist, Command: "cat"},
		},
	}
	withStubRunner(t, stub)

	_, err := runInitCmd(t, "hunter2\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home",
		"--path", "/data",
		"--backend", "file",
		"--password-stdin",
	)
	require.NoError(t, err)

	// The init probe is wrapped in LogStart/LogEnd, so the persistent log (in the
	// isolated XDG state dir) records a start and a success-end line for `init`.
	cfg := config.Default()
	logPath, err := logging.ResolvePath(&cfg)
	require.NoError(t, err)
	data, err := os.ReadFile(logPath) //nolint:gosec // log path derived from isolated XDG state dir, not arbitrary input
	require.NoError(t, err)
	logged := string(data)
	assert.Contains(t, logged, "run start")
	assert.Contains(t, logged, "run end")
	assert.Contains(t, logged, `"command":"init"`)
	assert.Contains(t, logged, `"outcome":"success"`)
}

func TestInitVerifiesExistingRepoWithoutClobbering(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}} // `cat config` succeeds → repo exists
	withStubRunner(t, stub)

	out, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home",
		"--path", "/data",
		"--backend", "file",
		"--password-stdin",
	)
	require.NoError(t, err)

	// Only the probe ran; `restic init` was NOT invoked on an existing repo.
	require.Len(t, stub.calls, 1)
	assert.Equal(t, []string{"cat", "config"}, stub.calls[0])
	assert.Contains(t, out, "already initialized")
}

func TestInitStoresRESTCredentials(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	_, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home",
		"--path", "/data",
		"--backend", "file",
		"--rest-username", "alice",
		"--rest-password", "rsecret",
		"--password-stdin",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)

	user, err := store.Get(credentials.RESTUsername)
	require.NoError(t, err)
	assert.Equal(t, "alice", user)
	pass, err := store.Get(credentials.RESTPassword)
	require.NoError(t, err)
	assert.Equal(t, "rsecret", pass)
}

func TestInitRefusesToClobberExistingConfig(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	args := []string{
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home",
		"--path", "/data",
		"--backend", "file",
		"--password-stdin",
	}

	// First init succeeds.
	_, err := runInitCmd(t, "pw\n", args...)
	require.NoError(t, err)

	// Second init without --force refuses.
	out, err := runInitCmd(t, "pw\n", args...)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--force")
	assert.NotContains(t, out, "icebeam run")
}

func TestInitForceOverwritesExistingConfig(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	_, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/old",
		"--set", "home", "--path", "/data", "--backend", "file", "--password-stdin",
	)
	require.NoError(t, err)

	_, err = runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/new",
		"--set", "home", "--path", "/data", "--backend", "file", "--password-stdin",
		"--force",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/new", cfg.Repository.URL)
}

func TestInitWrongPasswordRetriesUntilCorrect(t *testing.T) {
	isolateXDG(t)

	// First probe reports a wrong password; the engine re-prompts only the
	// password and retries, where the second probe succeeds (existing repo).
	stub := &stubRunner{
		catQueue: []error{
			&restic.ExitError{Code: restic.ExitWrongPassword, Command: "cat"},
			nil,
		},
	}
	withStubRunner(t, stub)

	// The interactive stdin supplies the (wrong) password via --password-stdin,
	// then a corrected password at the re-prompt. Use the non-stdin path so the
	// re-prompt reads from stdin as a plain line.
	stdin := strings.Join([]string{
		"badpass",  // initial repository password prompt
		"goodpass", // re-prompt after wrong-password
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin,
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data", "--backend", "file",
	)
	require.NoError(t, err)

	// Two probes ran (wrong then correct); no `init` on an existing repo.
	require.Len(t, stub.calls, 2)
	assert.Equal(t, []string{"cat", "config"}, stub.calls[0])
	assert.Equal(t, []string{"cat", "config"}, stub.calls[1])
	assert.Contains(t, out, "rejected")
	assert.Contains(t, out, "already initialized")

	// The corrected password (not the rejected one) is what got persisted.
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "goodpass", got)
}

func TestInitGenericProbeFailureAbortsWithoutPersisting(t *testing.T) {
	isolateXDG(t)

	// A non-password, non-not-exist failure (e.g. repo locked / host
	// unreachable) surfaces restic's message and offers re-enter or abort.
	// Empty input at the yes/no prompt defaults to abort.
	stub := &stubRunner{
		results: map[string]error{
			"cat": &restic.ExitError{Code: restic.ExitRepoLocked, Command: "cat"},
		},
	}
	withStubRunner(t, stub)

	out, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data", "--backend", "file", "--password-stdin",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborted")
	require.Len(t, stub.calls, 1) // probe only; no `init`
	assert.Contains(t, out, "Could not reach repository")

	// Abort path leaves nothing behind: no config and no stored secret.
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	_, statErr := os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(statErr), "config must not be written on abort")

	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)
	_, getErr := store.Get(credentials.RepoPassword)
	assert.ErrorIs(t, getErr, credentials.ErrNotFound, "no secret must be persisted on abort")
}

func TestInitPasswordStdinRequiresAPassword(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	_, err := runInitCmd(t, "\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data", "--backend", "file", "--password-stdin",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no password")
}

func TestInitInteractivePromptsForMissingValues(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	// Non-TTY stdin: repo, set name, paths, then the password are read as plain
	// lines in prompt order.
	stdin := strings.Join([]string{
		"rest:https://nas.local:8000/icebeam",
		"laptop",
		"/home/me, /etc",
		"swordfish",
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin, "--backend", "file")
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/icebeam", cfg.Repository.URL)
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "laptop", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/home/me", "/etc"}, cfg.Sets[0].Paths)

	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "swordfish", got)

	assert.Contains(t, out, "Repository URL")
}

// dirOf returns the directory containing path; the file credential backend
// writes its secret files there.
func dirOf(t *testing.T, path string) string {
	t.Helper()
	return filepath.Dir(path)
}

// sanity: the temp XDG dir is honored so we never read the real machine config.
func TestInitUsesIsolatedConfigPath(t *testing.T) {
	base := isolateXDG(t)
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(cfgPath, base), "config path %s should be under temp XDG dir %s", cfgPath, base)
	_, statErr := os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(statErr))
}
