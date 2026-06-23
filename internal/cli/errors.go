package cli

import "fmt"

// errNotImplemented is returned by stubbed subcommands until their owning story
// supplies a real implementation.
func errNotImplemented(name string) error {
	return fmt.Errorf("%q is not implemented yet", name)
}
