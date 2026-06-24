package restic

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreForwardsArgs(t *testing.T) {
	t.Parallel()

	// The stub asserts the restore subcommand and its target/include/exclude args
	// arrive in order; the only path to the success exit is for all of them to be
	// present.
	stub := writeStub(t, `
[ "$1" = "restore" ] || { echo "missing restore subcommand" >&2; exit 2; }
[ "$2" = "latest" ] || { echo "missing snapshot selector" >&2; exit 2; }
[ "$3" = "--target" ] || { echo "missing --target" >&2; exit 2; }
[ "$4" = "/tmp/out" ] || { echo "missing target value" >&2; exit 2; }
[ "$5" = "--include" ] || { echo "missing --include" >&2; exit 2; }
[ "$6" = "/etc" ] || { echo "missing include value" >&2; exit 2; }
exit 0
`)
	r := newRunner(t, stub, nil)

	err := r.Restore(context.Background(), "latest", "--target", "/tmp/out", "--include", "/etc")
	require.NoError(t, err)
}

func TestRestoreSurfacesExitError(t *testing.T) {
	t.Parallel()

	// A non-existent repository (restic exit 10) must surface as an *ExitError so
	// the caller can map it to an icebeam exit code.
	stub := writeStub(t, `
echo "repository does not exist" >&2
exit 10
`)
	r := newRunner(t, stub, nil)

	err := r.Restore(context.Background(), "latest", "--target", "/tmp/out")
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.True(t, exitErr.IsRepoNotExist())
}

func TestDumpStreamsBinaryWithoutCorruption(t *testing.T) {
	t.Parallel()

	// The stub asserts the dump subcommand + snapshot/path args, then emits binary
	// content (including NUL and high bytes) on stdout. Dump must copy it through
	// unchanged.
	stub := writeStub(t, `
[ "$1" = "dump" ] || { echo "missing dump subcommand" >&2; exit 2; }
[ "$2" = "latest" ] || { echo "missing snapshot selector" >&2; exit 2; }
[ "$3" = "/etc/hosts" ] || { echo "missing path" >&2; exit 2; }
printf '\000\001\002\377hello\n\000world'
exit 0
`)
	r := newRunner(t, stub, nil)

	var buf bytes.Buffer
	err := r.Dump(context.Background(), &buf, "latest", "/etc/hosts")
	require.NoError(t, err)

	want := []byte{0x00, 0x01, 0x02, 0xff, 'h', 'e', 'l', 'l', 'o', '\n', 0x00, 'w', 'o', 'r', 'l', 'd'}
	assert.Equal(t, want, buf.Bytes())
}

func TestDumpSurfacesExitError(t *testing.T) {
	t.Parallel()

	// Dumping a directory or absent path fails (restic exit 1); it must surface as
	// an *ExitError so the caller maps the exit code and nothing partial is left as
	// success.
	stub := writeStub(t, `
echo "cannot dump path: is a directory" >&2
exit 1
`)
	r := newRunner(t, stub, nil)

	var buf bytes.Buffer
	err := r.Dump(context.Background(), &buf, "latest", "/some/dir")
	require.Error(t, err)
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ExitGeneric, exitErr.Code)
}
