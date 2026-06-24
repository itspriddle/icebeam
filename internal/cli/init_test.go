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
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "home", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/Users/josh/Documents", "/Users/josh/Projects"}, cfg.Sets[0].Paths)
	assert.Equal(t, []string{"**/node_modules"}, cfg.Sets[0].Exclude)
	assert.Equal(t, []string{"home"}, cfg.Sets[0].Tags)

	// Secret was stored via the file backend and never appears in any restic argv.
	store, err := credentials.Open(dirOf(t, cfgPath))
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
		"--rest-username", "alice",
		"--rest-password-stdin",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(dirOf(t, cfgPath))
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
		"--set", "home", "--path", "/data",
	)
	require.NoError(t, err)

	assert.Contains(t, out, "REST-server username")
	assert.Contains(t, out, "REST-server password")

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(dirOf(t, cfgPath))
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
		"--set", "home", "--path", "/data",
	)
	require.NoError(t, err)

	assert.NotContains(t, out, "REST-server username")
	assert.NotContains(t, out, "REST-server password")

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(dirOf(t, cfgPath))
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
		"--set", "home", "--path", "/data",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(dirOf(t, cfgPath))
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
		"--set", "home", "--path", "/data",
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

func TestInitStripsEmbeddedRESTCredentialsFromRepoURL(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}} // existing repo verified
	withStubRunner(t, stub)

	// A repo URL with embedded HTTP credentials. They must be stripped from the
	// stored URL and moved to the credential store; the repository password is
	// read from stdin.
	out, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://alice:rsecret@nas.local:8000/icebeam",
		"--set", "home", "--path", "/data",
		"--password-stdin",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)

	// The persisted URL carries no userinfo.
	assert.Equal(t, "rest:https://nas.local:8000/icebeam", cfg.Repository.URL)
	assert.NotContains(t, cfg.Repository.URL, "alice")
	assert.NotContains(t, cfg.Repository.URL, "rsecret")

	// The embedded credentials were moved into the credential store.
	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)
	user, err := store.Get(credentials.RESTUsername)
	require.NoError(t, err)
	assert.Equal(t, "alice", user)
	pass, err := store.Get(credentials.RESTPassword)
	require.NoError(t, err)
	assert.Equal(t, "rsecret", pass)

	// The probe ran with the embedded credentials (no separate probe failure), and
	// no secret reached restic's argv.
	for _, call := range stub.calls {
		joined := strings.Join(call, " ")
		assert.NotContains(t, joined, "rsecret")
		assert.NotContains(t, joined, "alice")
	}

	// A warning was shown, but it leaks no secret; the summary prints only the
	// stripped URL.
	assert.Contains(t, out, "credentials")
	assert.NotContains(t, out, "rsecret")
	assert.NotContains(t, out, "alice:rsecret")

	// The raw config file on disk never contains the secret.
	raw, err := os.ReadFile(cfgPath) //nolint:gosec // config path derived from isolated XDG dir, not arbitrary input
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "rsecret")
	assert.NotContains(t, string(raw), "alice")
}

func TestInitFlagRESTUsernameWinsOverURLEmbedded(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	// An explicit --rest-username takes precedence over the URL-embedded username;
	// the URL-embedded password is still used since none was supplied otherwise.
	_, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://alice:rsecret@nas.local:8000/icebeam",
		"--set", "home", "--path", "/data",
		"--rest-username", "bob",
		"--password-stdin",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)

	user, err := store.Get(credentials.RESTUsername)
	require.NoError(t, err)
	assert.Equal(t, "bob", user, "explicit --rest-username must win over URL-embedded one")
	pass, err := store.Get(credentials.RESTPassword)
	require.NoError(t, err)
	assert.Equal(t, "rsecret", pass)
}

func TestInitPlainRESTURLIsUnaffected(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	// A plain rest: URL with no embedded credentials: the URL is stored verbatim,
	// no REST credentials are stored, and no relocation warning is shown.
	stdin := strings.Join([]string{
		"", "", "", "", // retention → defaults
		"",   // REST username (blank → none)
		"",   // REST password (blank → none)
		"pw", // repository password
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin,
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/icebeam", cfg.Repository.URL)

	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)
	_, err = store.Get(credentials.RESTUsername)
	require.ErrorIs(t, err, credentials.ErrNotFound)
	_, err = store.Get(credentials.RESTPassword)
	require.ErrorIs(t, err, credentials.ErrNotFound)

	// No relocation warning for a URL that carried no credentials.
	assert.NotContains(t, out, "moved to the credential store")
}

func TestInitPersistsNonDefaultRetentionFlags(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{
		results: map[string]error{
			"cat": &restic.ExitError{Code: restic.ExitRepoNotExist, Command: "cat"},
		},
	}
	withStubRunner(t, stub)

	// Pass non-default keep-* values via flags (the defaults are 7/4/12/3). Using
	// --password-stdin makes the run fully scripted so the retention prompts are
	// suppressed and the flag values flow straight through to config.Retention; a
	// value dropped before persistence would be caught here.
	_, err := runInitCmd(t, "hunter2\n",
		"--repo", "sftp:user@host:/srv/backup",
		"--set", "home", "--path", "/data",
		"--keep-daily", "99", "--keep-weekly", "8",
		"--keep-monthly", "24", "--keep-yearly", "5",
		"--password-stdin",
	)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)

	assert.Equal(t, 99, cfg.Retention.KeepDaily)
	assert.Equal(t, 8, cfg.Retention.KeepWeekly)
	assert.Equal(t, 24, cfg.Retention.KeepMonthly)
	assert.Equal(t, 5, cfg.Retention.KeepYearly)
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
		"--password-stdin",
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
	store, err := credentials.Open(dirOf(t, cfgPath))
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

// seedExistingConfigWithREST runs init once against a REST repo, storing a REST
// username and password alongside the repository password, so a re-run test can
// exercise the changed-REST-credential skip-probe logic. It returns the config
// path.
func seedExistingConfigWithREST(t *testing.T, stub *stubRunner) string {
	t.Helper()
	// --rest-username supplies the username; --rest-password-stdin reads the REST
	// password from stdin, then the repository password is the next stdin line.
	_, err := runInitCmd(t, "rsecret\npw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data",
		"--rest-username", "alice",
		"--rest-password-stdin",
	)
	require.NoError(t, err)
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	return cfgPath
}

func TestInitRerunChangedRESTPasswordReVerifies(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	cfgPath := seedExistingConfigWithREST(t, stub)

	probesAfterSeed := len(stub.calls)

	// Re-run keeping the repo URL and repository password, but entering a NEW REST
	// password. Even though the repo URL + repo password are unchanged, the changed
	// REST credential must force the probe so a wrong new REST password is caught
	// before it is persisted.
	stdin := strings.Join([]string{
		"",             // repo URL → keep
		"",             // set name → keep
		"",             // paths → keep
		"", "", "", "", // retention → keep existing
		"",          // REST username → keep stored ("alice")
		"newsecret", // REST password → change
		"",          // repo password → keep existing
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin)
	require.NoError(t, err)
	assert.NotContains(t, out, "skipping re-verification")

	// A probe ran on the changed-REST-password re-run.
	require.Greater(t, len(stub.calls), probesAfterSeed)
	assert.Equal(t, []string{"cat", "config"}, stub.calls[probesAfterSeed])

	// The new REST password was persisted after the probe verified it.
	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)
	pass, err := store.Get(credentials.RESTPassword)
	require.NoError(t, err)
	assert.Equal(t, "newsecret", pass)
}

func TestInitRerunWrongNewRESTCredentialAbortsWithoutPersisting(t *testing.T) {
	isolateXDG(t)

	// The seed probe succeeds; the re-run probe (with the changed REST password)
	// fails with a generic error (a REST 401 has no distinct restic exit code, so
	// it falls into the generic-error branch). Empty input at the re-enter prompt
	// defaults to abort, so nothing new is persisted.
	stub := &stubRunner{
		catQueue: []error{
			nil, // seed probe → existing repo verified
			&restic.ExitError{Code: restic.ExitRepoLocked, Command: "cat"}, // re-run probe → generic failure
		},
	}
	withStubRunner(t, stub)
	cfgPath := seedExistingConfigWithREST(t, stub)

	probesAfterSeed := len(stub.calls)

	// Keep everything but enter a wrong new REST password. The probe fails and the
	// re-enter prompt is answered blank, defaulting to abort.
	stdin := strings.Join([]string{
		"",             // repo URL → keep
		"",             // set name → keep
		"",             // paths → keep
		"", "", "", "", // retention → keep existing
		"",         // REST username → keep stored
		"wrongnew", // REST password → change (wrong)
		"",         // repo password → keep existing
		"",         // re-enter? → blank → abort
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborted")
	assert.Contains(t, out, "Could not reach repository")

	// The probe ran on the re-run (so the wrong REST credential was tested).
	require.Greater(t, len(stub.calls), probesAfterSeed)
	assert.Equal(t, []string{"cat", "config"}, stub.calls[probesAfterSeed])

	// The wrong new REST password was NOT persisted: the stored value is still the
	// original from the seed run.
	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)
	pass, err := store.Get(credentials.RESTPassword)
	require.NoError(t, err)
	assert.Equal(t, "rsecret", pass)
}

func TestInitRerunNoRESTChangeStillSkipsProbe(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)
	seedExistingConfigWithREST(t, stub)

	probesAfterSeed := len(stub.calls)

	// Re-run keeping every secret, including the stored REST credentials. With the
	// repo URL, repo password, and REST credentials all unchanged, the probe is
	// skipped — no regression for the common no-op re-run.
	stdin := strings.Join([]string{
		"",             // repo URL → keep
		"",             // set name → keep
		"",             // paths → keep
		"", "", "", "", // retention → keep existing
		"", // REST username → keep stored
		"", // REST password → keep stored
		"", // repo password → keep existing
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin)
	require.NoError(t, err)
	assert.Contains(t, out, "skipping re-verification")

	// No additional probe ran on the fully-unchanged re-run.
	assert.Len(t, stub.calls, probesAfterSeed)
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
		"--set", "home", "--path", "/data",
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
	store, err := credentials.Open(dirOf(t, cfgPath))
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
		"--set", "home", "--path", "/data", "--password-stdin",
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

	store, err := credentials.Open(dirOf(t, cfgPath))
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
		"--set", "home", "--path", "/data", "--password-stdin",
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

	out, err := runInitCmd(t, stdin)
	require.NoError(t, err)

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	cfg, err := config.LoadFile(cfgPath)
	require.NoError(t, err)
	assert.Equal(t, "rest:https://nas.local:8000/icebeam", cfg.Repository.URL)
	require.Len(t, cfg.Sets, 1)
	assert.Equal(t, "laptop", cfg.Sets[0].Name)
	assert.Equal(t, []string{"/home/me", "/etc"}, cfg.Sets[0].Paths)

	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, "swordfish", got)

	assert.Contains(t, out, "Repository URL")
}

func TestInitGeneratePasswordFlagNonInteractive(t *testing.T) {
	isolateXDG(t)

	// A deterministic all-zero reader makes generatePassword yield a known value:
	// generatedPasswordLength repetitions of the first charset character.
	withRandReader(t, bytes.NewReader(make([]byte, generatedPasswordLength)))
	wantPW := strings.Repeat(string(passwordCharset[0]), generatedPasswordLength)

	stub := &stubRunner{
		results: map[string]error{
			"cat": &restic.ExitError{Code: restic.ExitRepoNotExist, Command: "cat"},
		},
	}
	withStubRunner(t, stub)

	// Fully non-interactive: every value via flags (a non-REST repo so no REST
	// prompts), the four retention flags so nothing prompts, and --generate-password
	// to create the repository password. No stdin is consumed.
	out, err := runInitCmd(t, "",
		"--repo", "sftp:user@host:/srv/backup",
		"--set", "home", "--path", "/data",
		"--keep-daily", "7", "--keep-weekly", "4",
		"--keep-monthly", "12", "--keep-yearly", "3",
		"--generate-password",
	)
	require.NoError(t, err)

	// The probe then init ran; the generated password is what got stored.
	require.Len(t, stub.calls, 2)
	assert.Equal(t, []string{"cat", "config"}, stub.calls[0])
	assert.Equal(t, []string{"init"}, stub.calls[1])

	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, wantPW, got)

	// The generated password was shown exactly once with the unrecoverable warning,
	// and never appears on a restic argv.
	assert.Contains(t, out, wantPW)
	assert.Equal(t, 1, strings.Count(out, wantPW), "generated password must be displayed exactly once")
	assert.Contains(t, out, "NOT recoverable")
	assert.Contains(t, out, "SAVE IT NOW")
	for _, call := range stub.calls {
		assert.NotContains(t, strings.Join(call, " "), wantPW)
	}

	// The generated password is never written to config.toml.
	data, err := os.ReadFile(cfgPath) //nolint:gosec // path derived from isolated XDG dir
	require.NoError(t, err)
	assert.NotContains(t, string(data), wantPW)
}

func TestInitGeneratePasswordInteractiveBlankAnswer(t *testing.T) {
	isolateXDG(t)

	withRandReader(t, bytes.NewReader(make([]byte, generatedPasswordLength)))
	wantPW := strings.Repeat(string(passwordCharset[0]), generatedPasswordLength)

	stub := &stubRunner{results: map[string]error{}} // existing repo verified
	withStubRunner(t, stub)

	// Non-TTY stdin for a non-REST repo: the four retention prompts kept at
	// defaults, then the repository password answered BLANK, which selects
	// generation.
	stdin := strings.Join([]string{
		"", "", "", "", // retention → defaults
		"", // repository password → blank → generate
	}, "\n") + "\n"

	out, err := runInitCmd(t, stdin,
		"--repo", "sftp:user@host:/srv/backup",
		"--set", "home", "--path", "/data",
	)
	require.NoError(t, err)

	// The blank answer generated and stored a password (not an empty one).
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	store, err := credentials.Open(dirOf(t, cfgPath))
	require.NoError(t, err)
	got, err := store.Get(credentials.RepoPassword)
	require.NoError(t, err)
	assert.Equal(t, wantPW, got)
	assert.NotEmpty(t, got)

	assert.Contains(t, out, "leave blank to generate")
	assert.Contains(t, out, wantPW)
	assert.Contains(t, out, "NOT recoverable")
}

func TestInitGeneratePasswordRejectedWithStdinFlag(t *testing.T) {
	isolateXDG(t)

	stub := &stubRunner{results: map[string]error{}}
	withStubRunner(t, stub)

	out, err := runInitCmd(t, "pw\n",
		"--repo", "rest:https://nas.local:8000/icebeam",
		"--set", "home", "--path", "/data",
		"--generate-password", "--password-stdin",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
	assert.NotContains(t, out, "icebeam run")

	// Nothing was written: the check runs before any persist.
	cfgPath, err := config.ConfigPath()
	require.NoError(t, err)
	_, statErr := os.Stat(cfgPath)
	assert.True(t, os.IsNotExist(statErr), "config must not be written when both flags are passed")
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
