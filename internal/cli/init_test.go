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

	// --rest-username supplies the (non-secret) username; --rest-password-stdin
	// reads the REST password from stdin first, then the repository password is
	// read as the next stdin line (no --password-stdin, the two are exclusive).
	_, err := runInitCmd(t, "rsecret\npw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home",
		"--path", "/data",
		"--backend", "file",
		"--rest-username", "alice",
		"--rest-password-stdin",
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

	// Neither secret ever appears in a restic argv.
	for _, call := range stub.calls {
		joined := strings.Join(call, " ")
		assert.NotContains(t, joined, "rsecret")
		assert.NotContains(t, joined, "pw")
	}
}

func TestInitPromptsForRESTCredentialsOnRESTRepo(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	// Non-TTY stdin: the four retention prompts (kept at their defaults with empty
	// lines), then REST username (visible/optional), REST password (hidden/
	// optional, read as a plain line off-TTY), then the repository password — in
	// prompt order.
	stdin := strings.Join([]string{
		"", "", "", "", // retention (keep daily/weekly/monthly/yearly) → defaults
		"alice",   // REST username
		"rsecret", // REST password
		"pw",      // repository password
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin,
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data", "--backend", "file",
	)
	require.NoError(t, err)

	assert.Contains(t, out, "REST-server username")
	assert.Contains(t, out, "REST-server password")

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

func TestInitSkipsRESTPromptsForNonRESTRepo(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	// A non-REST (sftp) repository: the four retention prompts (kept at defaults
	// with blank lines) then the repository password are prompted; the REST
	// username/password prompts never appear.
	stdin := strings.Join([]string{
		"", "", "", "", // retention → defaults
		"pw", // repository password
	}, "\n") + "\n"
	out, err := runInitCmd(t, stdin,
		"--repo", "sftp:user@host:/srv/backup",
		"--set", "home", "--path", "/data", "--backend", "file",
	)
	require.NoError(t, err)

	assert.NotContains(t, out, "REST-server username")
	assert.NotContains(t, out, "REST-server password")

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)

	// No REST credentials were stored for a non-REST repository.
	_, err = store.Get(credentials.RESTUsername)
	require.ErrorIs(t, err, credentials.ErrNotFound)
	_, err = store.Get(credentials.RESTPassword)
	assert.ErrorIs(t, err, credentials.ErrNotFound)
}

func TestInitAllowsEmptyRESTCredentials(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	// A REST server with no HTTP auth: blank username and blank password are
	// accepted, and nothing is stored for them.
	stdin := strings.Join([]string{
		"", "", "", "", // retention → defaults
		"",   // REST username (blank → none)
		"",   // REST password (blank → none)
		"pw", // repository password
	}, "\n") + "\n"

	_, err := runInitCmd(t, stdin,
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data", "--backend", "file",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)

	_, err = store.Get(credentials.RESTUsername)
	require.ErrorIs(t, err, credentials.ErrNotFound)
	_, err = store.Get(credentials.RESTPassword)
	assert.ErrorIs(t, err, credentials.ErrNotFound)
}

func TestInitRejectsBothStdinSecretFlags(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	out, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data", "--backend", "file",
		"--password-stdin", "--rest-password-stdin",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only one secret")
	assert.NotContains(t, out, "icebeam run")

	// Nothing was written: the mutual-exclusion check runs before any persist.
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	_, statErr := os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(statErr), "config must not be written when both stdin flags are passed")
}

// seedExistingConfig runs init once to leave a complete config and stored
// secrets on the isolated XDG dir, returning the config path so a re-run test can
// assert against the pre-filled defaults.
func seedExistingConfig(t *testing.T, stub *stubRunner) string {
	t.Helper()
	_, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data",
		"--exclude", "**/node_modules", "--tag", "home",
		"--backend", "file", "--password-stdin",
	)
	require.NoError(t, err)
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	return cfgPath
}

func TestInitRerunPreFillsFromExistingConfig(t *testing.T) {
	isolateXDG(t)

	// Existing repo verified on the first run, then again on the re-run.
	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	cfgPath := seedExistingConfig(t, stub)

	// Re-run interactively, accepting every pre-filled default with empty input
	// (repo URL, set name, paths, retention, REST username, REST password, repo
	// password), but changing the set name to "laptop".
	stdin := strings.Join([]string{
		"",             // repo URL → keep
		"laptop",       // set name → change
		"",             // paths → keep
		"", "", "", "", // retention → keep existing (defaults)
		"", // REST username → keep (none stored → blank)
		"", // REST password → keep (none stored → blank)
		"", // repo password → keep existing
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin)
	require.NoError(t, err)
	assert.Contains(t, out, "Existing config found")

	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	// Changed value applied; carried-forward values preserved.
	assert.Equal(t, "rest:https://nas.local:8000/icebeam", cfg.Repository.URL)
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "laptop", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/data"}, cfg.Sets[0].Paths)
	assert.Equal(t, []string{"**/node_modules"}, cfg.Sets[0].Exclude)
	assert.Equal(t, []string{"home"}, cfg.Sets[0].Tags)
}

func TestInitRerunKeepsExistingSecretAndSkipsProbe(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	cfgPath := seedExistingConfig(t, stub)

	probesAfterSeed := len(stub.calls)

	// Re-run accepting all defaults including "keep existing" for the password.
	// With the repo URL and password unchanged, the engine skips re-verification:
	// no further probe runs. The lines are repo URL, set name, paths, the four
	// retention prompts, REST username, REST password, and the repo password — all
	// kept with empty input.
	stdin := strings.Join([]string{"", "", "", "", "", "", "", "", "", ""}, "\n") + "\n"
	out, err := runInitCmd(t, stdin)
	require.NoError(t, err)
	assert.Contains(t, out, "skipping re-verification")

	// No additional restic call (probe) was made on the unchanged re-run.
	assert.Len(t, stub.calls, probesAfterSeed)

	// The stored password is intact.
	store, err := credentials.Open(credentials.BackendFile, dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "pw", got)
}

func TestInitRerunChangedRepoURLReVerifies(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	seedExistingConfig(t, stub)

	probesAfterSeed := len(stub.calls)

	// Change only the repository URL; keep everything else (including the stored
	// password). A changed repo URL must re-verify even though the password is
	// kept, so a probe runs.
	stdin := strings.Join([]string{
		"rest:https://nas.local:8000/moved", // repo URL → change
		"",                                  // set name → keep
		"",                                  // paths → keep
		"", "", "", "",                      // retention → keep existing
		"", // REST username → keep
		"", // REST password → keep
		"", // repo password → keep existing
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin)
	require.NoError(t, err)
	assert.NotContains(t, out, "skipping re-verification")

	// A probe ran on the changed-URL re-run.
	require.Greater(t, len(stub.calls), probesAfterSeed)
	assert.Equal(t, []string{"cat", "config"}, stub.calls[probesAfterSeed])

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/moved", cfg.Repository.URL)
}

func TestInitRerunFlagOverridesLoadedDefault(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	seedExistingConfig(t, stub)

	// A flag-supplied value overrides the loaded default and suppresses its
	// prompt; the only prompt left is the (kept) password. The repo URL changed,
	// so the probe runs.
	out, err := runInitCmd(t, "\n",
		"--repo", "rest:https://nas.local:8000/flagged",
	)
	require.NoError(t, err)
	// The repo URL prompt was suppressed by the flag.
	assert.NotContains(t, out, "Repository URL")

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/flagged", cfg.Repository.URL)
	// Carried-forward set values from the loaded config are unchanged.
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "home", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/data"}, cfg.Sets[0].Paths)
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

	// The interactive stdin supplies the (blank) REST credentials for the REST
	// repo, then the (wrong) repository password, then a corrected password at the
	// re-prompt. Use the non-stdin path so the re-prompt reads from stdin as a
	// plain line.
	stdin := strings.Join([]string{
		"", "", "", "", // retention → defaults
		"",         // REST username (blank → none)
		"",         // REST password (blank → none)
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

	// Non-TTY stdin: repo, set name, paths, the four retention prompts (kept at
	// defaults), the (blank) REST username/password for the REST repo, then the
	// repository password are read as plain lines in prompt order.
	stdin := strings.Join([]string{
		"rest:https://nas.local:8000/icebeam",
		"laptop",
		"/home/me, /etc",
		"", "", "", "", // retention → defaults
		"", // REST username (blank → none)
		"", // REST password (blank → none)
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
