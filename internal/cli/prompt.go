package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/itspriddle/icebeam/internal/config"
	"github.com/itspriddle/icebeam/internal/logging"
)

// errInputEnded reports that stdin reached end-of-input before a required prompt
// received a value. The required-value prompts (ask, askList, askSecret) return
// it instead of re-prompting, so an exhausted or incomplete stdin fails fast
// rather than looping forever (which once exhausted memory until the machine
// restarted).
var errInputEnded = errors.New("end of input: no value provided")

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
// until a non-empty answer is given. It returns errInputEnded if stdin reaches
// end-of-input before a value is entered, rather than re-prompting indefinitely.
func (p *prompter) ask(label string) (string, error) {
	for {
		p.printf("%s: ", label)
		line, eof, err := p.readLine()
		if err != nil {
			return "", err
		}
		if eof {
			return "", errInputEnded
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
	line, _, err := p.readLine()
	if err != nil {
		return "", err
	}
	if line == "" {
		return def, nil
	}
	return line, nil
}

// askIntDefault prompts for a non-negative integer, showing the default, and
// returns the entered value or the default when the user submits an empty line.
// A non-numeric or negative answer re-prompts rather than aborting setup.
func (p *prompter) askIntDefault(label string, def int) (int, error) {
	for {
		p.printf("%s [%d]: ", label, def)
		line, eof, err := p.readLine()
		if err != nil {
			return 0, err
		}
		if eof || line == "" {
			return def, nil
		}
		n, err := strconv.Atoi(line)
		if err != nil || n < 0 {
			p.println("Please enter a non-negative whole number.")
			continue
		}
		return n, nil
	}
}

// askYesNo prompts a yes/no question, showing the default in the [Y/n] / [y/N]
// hint, and returns the answer. An empty line (or end of input) selects the
// default, so the non-interactive path never blocks; an unrecognized answer
// re-prompts.
func (p *prompter) askYesNo(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		p.printf("%s [%s]: ", label, hint)
		line, eof, err := p.readLine()
		if err != nil {
			return false, err
		}
		if eof {
			return def, nil
		}
		switch strings.ToLower(line) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			p.println(`Please answer "y" or "n".`)
		}
	}
}

// askOptional prints a prompt and returns the trimmed line the user enters,
// allowing an empty answer (unlike ask, which repeats until non-empty). It is
// used for genuinely optional input such as a REST username for a server with no
// HTTP auth.
func (p *prompter) askOptional(label string) (string, error) {
	p.printf("%s (optional, leave blank for none): ", label)
	line, _, err := p.readLine()
	return line, err
}

// askSecretOptional prompts for a secret without echoing it when the input is a
// terminal, falling back to a plain line read otherwise. Unlike askSecret it
// allows an empty answer, for an optional secret such as a REST password when
// the server has no HTTP auth.
func (p *prompter) askSecretOptional(label string) (string, error) {
	p.printf("%s (optional, leave blank for none): ", label)

	if isTerminal(p.in) {
		secret, err := readHiddenPassword(p.in)
		p.println() // ReadPassword swallows the newline the user typed.
		return secret, err
	}
	line, _, err := p.readLine()
	return line, err
}

// askList prompts for a comma-separated list and returns the non-empty,
// trimmed elements, repeating until at least one is given. It returns
// errInputEnded if stdin reaches end-of-input before a value is entered, rather
// than re-prompting indefinitely.
func (p *prompter) askList(label string) ([]string, error) {
	for {
		p.printf("%s: ", label)
		line, eof, err := p.readLine()
		if err != nil {
			return nil, err
		}
		if eof {
			return nil, errInputEnded
		}
		items := splitList(line)
		if len(items) > 0 {
			return items, nil
		}
		p.println("At least one value is required.")
	}
}

// askListDefault prompts for a comma-separated list, showing the current values
// as the default, and returns the entered list or the default when the user
// submits an empty line. Used when re-running setup to pre-fill from an existing
// config without forcing the user to re-type unchanged paths.
func (p *prompter) askListDefault(label string, def []string) ([]string, error) {
	p.printf("%s [%s]: ", label, strings.Join(def, ", "))
	line, _, err := p.readLine()
	if err != nil {
		return nil, err
	}
	items := splitList(line)
	if len(items) == 0 {
		return def, nil
	}
	return items, nil
}

// askSecret prompts for a secret without echoing it when the input is a
// terminal, falling back to a plain line read otherwise. It repeats until a
// non-empty value is entered. On the non-terminal (piped) path it returns
// errInputEnded if stdin reaches end-of-input before a value is entered, rather
// than re-prompting indefinitely.
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
			var eof bool
			secret, eof, err = p.readLine()
			if err == nil && eof {
				return "", errInputEnded
			}
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

// askSecretKeep prompts for a secret when one is already stored, offering a
// "keep existing" default: an empty answer keeps the current value (kept=true,
// secret=""), and any entered value replaces it (kept=false). It is used when
// re-running setup so the user can change one setting without re-typing a secret
// they want to leave alone. The secret is never echoed on a terminal.
func (p *prompter) askSecretKeep(label string) (secret string, kept bool, err error) {
	p.printf("%s [keep existing, leave blank to keep]: ", label)

	if isTerminal(p.in) {
		secret, err = readHiddenPassword(p.in)
		p.println() // ReadPassword swallows the newline the user typed.
	} else {
		secret, _, err = p.readLine()
	}
	if err != nil {
		return "", false, err
	}
	if secret == "" {
		return "", true, nil
	}
	return secret, false, nil
}

// readSecretLine reads one secret line from the prompter's input, trimming only
// the trailing newline (a secret may contain leading/trailing spaces, unlike a
// trimmed answer). It reads through the prompter's shared reader so a piped
// secret followed by an interactive prompt does not lose buffered input. EOF is
// treated as end of input. errCtx labels the wrapped read error.
func (p *prompter) readSecretLine(errCtx string) (string, error) {
	line, err := p.r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%s: %w", errCtx, err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readLine reads a single line and trims surrounding whitespace. It reports
// end-of-input distinctly via eof: eof is true only when EOF was reached with no
// further content on this call (a final line lacking a trailing newline is
// returned normally, with eof false; the following call then reports eof). This
// lets the required-value prompts terminate on an exhausted stdin instead of
// looping forever.
func (p *prompter) readLine() (line string, eof bool, err error) {
	raw, err := p.r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, fmt.Errorf("read input: %w", err)
	}
	if errors.Is(err, io.EOF) && raw == "" {
		return "", true, nil
	}
	return strings.TrimSpace(raw), false, nil
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
