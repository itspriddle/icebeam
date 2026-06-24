package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestPrompter builds a prompter over the given stdin contents and a discard
// output. The input is a *strings.Reader (not an *os.File), so isTerminal
// reports false and the secret prompts take the plain-line path — the path that
// matters for the exhausted-stdin hardening.
func newTestPrompter(stdin string) *prompter {
	return newPrompter(strings.NewReader(stdin), io.Discard)
}

// TestRequiredPromptsTerminateOnEndOfInput asserts that the required-value
// prompts return errInputEnded promptly when stdin is exhausted, rather than
// looping forever and exhausting memory (the regression this story guards). Each
// subtest must complete; the package -timeout is the backstop against a
// reintroduced loop.
func TestRequiredPromptsTerminateOnEndOfInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(p *prompter) error
	}{
		{
			name: "ask",
			call: func(p *prompter) error {
				_, err := p.ask("Repository URL")
				return err
			},
		},
		{
			name: "askList",
			call: func(p *prompter) error {
				_, err := p.askList("Paths")
				return err
			},
		},
		{
			name: "askSecret",
			call: func(p *prompter) error {
				_, err := p.askSecret("Repository password")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run("empty stdin/"+tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.call(newTestPrompter(""))
			require.ErrorIs(t, err, errInputEnded)
		})

		t.Run("blank lines then EOF/"+tt.name, func(t *testing.T) {
			t.Parallel()

			// Blank/whitespace lines are not a value; once exhausted the prompt
			// must end rather than re-prompt forever.
			err := tt.call(newTestPrompter("\n   \n"))
			require.ErrorIs(t, err, errInputEnded)
		})
	}
}

// TestDefaultPromptsReturnDefaultOnEndOfInput pins the unchanged behavior of the
// default-bearing prompts: an exhausted stdin yields the default, never an
// error, so the non-interactive (flag-driven) path never blocks or fails.
func TestDefaultPromptsReturnDefaultOnEndOfInput(t *testing.T) {
	t.Parallel()

	t.Run("askDefault", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("").askDefault("Repo", "rest:https://nas/icebeam")
		require.NoError(t, err)
		assert.Equal(t, "rest:https://nas/icebeam", got)
	})

	t.Run("askIntDefault", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("").askIntDefault("Keep daily", 7)
		require.NoError(t, err)
		assert.Equal(t, 7, got)
	})

	t.Run("askYesNo true", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("").askYesNo("Proceed?", true)
		require.NoError(t, err)
		assert.True(t, got)
	})

	t.Run("askYesNo false", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("").askYesNo("Proceed?", false)
		require.NoError(t, err)
		assert.False(t, got)
	})

	t.Run("askListDefault", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("").askListDefault("Paths", []string{"/home", "/etc"})
		require.NoError(t, err)
		assert.Equal(t, []string{"/home", "/etc"}, got)
	})

	t.Run("askOptional", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("").askOptional("REST username")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("askSecretOptional", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("").askSecretOptional("REST password")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("askSecretKeep keeps existing", func(t *testing.T) {
		t.Parallel()

		secret, kept, err := newTestPrompter("").askSecretKeep("Repository password")
		require.NoError(t, err)
		assert.True(t, kept)
		assert.Empty(t, secret)
	})
}

// TestAskIntDefaultParsesAndReprompts covers askIntDefault's value-entry and
// re-prompt-on-invalid paths (the empty/EOF→default branch is pinned by
// TestDefaultPromptsReturnDefaultOnEndOfInput).
func TestAskIntDefaultParsesAndReprompts(t *testing.T) {
	t.Parallel()

	t.Run("entered value overrides the default", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("12\n").askIntDefault("Keep daily", 7)
		require.NoError(t, err)
		assert.Equal(t, 12, got)
	})

	t.Run("zero is accepted", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("0\n").askIntDefault("Keep daily", 7)
		require.NoError(t, err)
		assert.Zero(t, got)
	})

	t.Run("non-numeric answer re-prompts then accepts", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("nope\n5\n").askIntDefault("Keep daily", 7)
		require.NoError(t, err)
		assert.Equal(t, 5, got)
	})

	t.Run("negative answer re-prompts then accepts", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("-3\n5\n").askIntDefault("Keep daily", 7)
		require.NoError(t, err)
		assert.Equal(t, 5, got)
	})

	t.Run("invalid answer then EOF returns the default", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("nope\n").askIntDefault("Keep daily", 7)
		require.NoError(t, err)
		assert.Equal(t, 7, got)
	})
}

// TestAskSecretOrGenerateOffersGeneration pins the fresh-setup password prompt:
// a blank answer (or exhausted stdin) selects generation, and any entered value
// is used as-is. It never loops, so an exhausted stdin can never spin.
func TestAskSecretOrGenerateOffersGeneration(t *testing.T) {
	t.Parallel()

	t.Run("blank answer selects generation", func(t *testing.T) {
		t.Parallel()

		secret, generate, err := newTestPrompter("\n").askSecretOrGenerate()
		require.NoError(t, err)
		assert.True(t, generate)
		assert.Empty(t, secret)
	})

	t.Run("exhausted stdin selects generation", func(t *testing.T) {
		t.Parallel()

		secret, generate, err := newTestPrompter("").askSecretOrGenerate()
		require.NoError(t, err)
		assert.True(t, generate)
		assert.Empty(t, secret)
	})

	t.Run("entered value is used as-is", func(t *testing.T) {
		t.Parallel()

		secret, generate, err := newTestPrompter("hunter2\n").askSecretOrGenerate()
		require.NoError(t, err)
		assert.False(t, generate)
		assert.Equal(t, "hunter2", secret)
	})
}

// TestReadLineReportsEndOfInput covers readLine's distinct eof signal, including
// the edge case of a final line with no trailing newline (returned normally,
// eof on the following read).
func TestReadLineReportsEndOfInput(t *testing.T) {
	t.Parallel()

	t.Run("empty input is eof", func(t *testing.T) {
		t.Parallel()

		line, eof, err := newTestPrompter("").readLine()
		require.NoError(t, err)
		assert.True(t, eof)
		assert.Empty(t, line)
	})

	t.Run("line without trailing newline is returned, then eof", func(t *testing.T) {
		t.Parallel()

		p := newTestPrompter("value")
		line, eof, err := p.readLine()
		require.NoError(t, err)
		assert.False(t, eof)
		assert.Equal(t, "value", line)

		line, eof, err = p.readLine()
		require.NoError(t, err)
		assert.True(t, eof)
		assert.Empty(t, line)
	})

	t.Run("blank line is not eof", func(t *testing.T) {
		t.Parallel()

		line, eof, err := newTestPrompter("\nnext\n").readLine()
		require.NoError(t, err)
		assert.False(t, eof)
		assert.Empty(t, line)
	})
}

// TestRequiredPromptsAcceptValueBeforeEndOfInput confirms the hardening does not
// regress the normal path: a value supplied before EOF is returned, even when it
// is the final line and lacks a trailing newline.
func TestRequiredPromptsAcceptValueBeforeEndOfInput(t *testing.T) {
	t.Parallel()

	t.Run("ask reads a value lacking a trailing newline", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("rest:https://nas/icebeam").ask("Repo")
		require.NoError(t, err)
		assert.Equal(t, "rest:https://nas/icebeam", got)
	})

	t.Run("askSecret reads a value then could end", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("s3cr3t\n").askSecret("Password")
		require.NoError(t, err)
		assert.Equal(t, "s3cr3t", got)
	})

	t.Run("askList reads a value after a re-prompt", func(t *testing.T) {
		t.Parallel()

		got, err := newTestPrompter("\n/home, /etc\n").askList("Paths")
		require.NoError(t, err)
		assert.Equal(t, []string{"/home", "/etc"}, got)
	})
}
