package config

import (
	"fmt"
	"regexp"
)

// safeIdentifier matches a permissible backup-set name: it must start with a
// letter or underscore and contain only letters, digits, underscores, and
// hyphens. This keeps set names usable as tags and CLI arguments.
var safeIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Validate checks the config for structural problems, naming the offending
// field in each error so a user can correct their config.toml.
func (c *Config) Validate() error {
	if c.Repository.URL == "" {
		return fmt.Errorf("repository.url: must not be empty")
	}

	if len(c.Sets) == 0 {
		return fmt.Errorf("set: at least one backup set must be defined")
	}

	seen := make(map[string]struct{}, len(c.Sets))
	for i, s := range c.Sets {
		if s.Name == "" {
			return fmt.Errorf("set[%d].name: must not be empty", i)
		}
		if !safeIdentifier.MatchString(s.Name) {
			return fmt.Errorf(
				"set[%d].name: %q is not a safe identifier (use letters, digits, _ or -, starting with a letter or _)",
				i, s.Name,
			)
		}
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("set[%d].name: duplicate set name %q", i, s.Name)
		}
		seen[s.Name] = struct{}{}

		if len(s.Paths) == 0 {
			return fmt.Errorf("set[%d].paths: set %q must list at least one path", i, s.Name)
		}
	}

	return nil
}
