package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/logging"
)

// prompter reads interactive answers from a reader and writes prompt text to a
// writer. It is the seam between the init command and the terminal so the
// non-interactive (flag-driven) path can be exercised without a TTY.
type prompter struct {
	in  io.Reader
	out io.Writer
	r   *bufio.Reader
}

// newPrompter builds a prompter over the given input and output streams.
func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: in, out: out, r: bufio.NewReader(in)}
}

// printf writes formatted status text to the prompter's output. Output errors on
// the user's terminal are not actionable (the command has already done its work),
// so they are intentionally ignored here at this single point.
func (p *prompter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.out, format, args...)
}

// println writes a status line to the prompter's output. Output errors are
// intentionally ignored (see printf).
func (p *prompter) println(args ...any) {
	_, _ = fmt.Fprintln(p.out, args...)
}

// ask prints a prompt and returns the trimmed line the user enters, repeating
// until a non-empty answer is given.
func (p *prompter) ask(label string) (string, error) {
	for {
		p.printf("%s: ", label)
		line, err := p.readLine()
		if err != nil {
			return "", err
		}
		if line != "" {
			return line, nil
		}
		p.println("A value is required.")
	}
}

// askDefault prints a prompt showing a default and returns the entered value, or
// the default when the user submits an empty line.
func (p *prompter) askDefault(label, def string) (string, error) {
	p.printf("%s [%s]: ", label, def)
	line, err := p.readLine()
	if err != nil {
		return "", err
	}
	if line == "" {
		return def, nil
	}
	return line, nil
}

// askList prompts for a comma-separated list and returns the non-empty,
// trimmed elements, repeating until at least one is given.
func (p *prompter) askList(label string) ([]string, error) {
	for {
		p.printf("%s: ", label)
		line, err := p.readLine()
		if err != nil {
			return nil, err
		}
		items := splitList(line)
		if len(items) > 0 {
			return items, nil
		}
		p.println("At least one value is required.")
	}
}

// askSecret prompts for a secret without echoing it when the input is a
// terminal, falling back to a plain line read otherwise. It repeats until a
// non-empty value is entered.
func (p *prompter) askSecret(label string) (string, error) {
	for {
		p.printf("%s: ", label)

		var (
			secret string
			err    error
		)
		if isTerminal(p.in) {
			secret, err = readHiddenPassword(p.in)
			p.println() // ReadPassword swallows the newline the user typed.
		} else {
			secret, err = p.readLine()
		}
		if err != nil {
			return "", err
		}
		if secret != "" {
			return secret, nil
		}
		p.println("A value is required.")
	}
}

// readLine reads a single line, trims surrounding whitespace, and treats EOF as
// the end of input (returning whatever was read so far).
func (p *prompter) readLine() (string, error) {
	line, err := p.r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// splitList splits a comma-separated string into trimmed, non-empty parts.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveLogPath returns the log path for the summary, delegating to the
// logging package's resolution so it matches what the logger will actually use.
func resolveLogPath(cfg *config.Config) (string, error) {
	return logging.ResolvePath(cfg)
}
