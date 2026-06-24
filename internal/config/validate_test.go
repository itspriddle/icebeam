package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAcceptsWellFormedConfig(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	require.NoError(t, cfg.Validate())
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*Config)
		wantField string
	}{
		{
			name:      "empty repository url",
			mutate:    func(c *Config) { c.Repository.URL = "" },
			wantField: "repository.url",
		},
		{
			name:      "no sets",
			mutate:    func(c *Config) { c.Sets = nil },
			wantField: "set",
		},
		{
			name:      "negative keep_daily",
			mutate:    func(c *Config) { c.Retention.KeepDaily = -1 },
			wantField: "retention.keep_daily",
		},
		{
			name:      "negative keep_weekly",
			mutate:    func(c *Config) { c.Retention.KeepWeekly = -1 },
			wantField: "retention.keep_weekly",
		},
		{
			name:      "negative keep_monthly",
			mutate:    func(c *Config) { c.Retention.KeepMonthly = -1 },
			wantField: "retention.keep_monthly",
		},
		{
			name:      "negative keep_yearly",
			mutate:    func(c *Config) { c.Retention.KeepYearly = -1 },
			wantField: "retention.keep_yearly",
		},
		{
			name: "set with no paths",
			mutate: func(c *Config) {
				c.Sets[0].Paths = nil
			},
			wantField: "set[0].paths",
		},
		{
			name: "set with empty name",
			mutate: func(c *Config) {
				c.Sets[0].Name = ""
			},
			wantField: "set[0].name",
		},
		{
			name: "set name not a safe identifier",
			mutate: func(c *Config) {
				c.Sets[0].Name = "has spaces"
			},
			wantField: "set[0].name",
		},
		{
			name: "set name starting with digit",
			mutate: func(c *Config) {
				c.Sets[0].Name = "1home"
			},
			wantField: "set[0].name",
		},
		{
			name: "duplicate set names",
			mutate: func(c *Config) {
				c.Sets = append(c.Sets, Set{
					Name:  "home",
					Paths: []string{"/other"},
				})
			},
			wantField: "set[1].name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantField,
				"validation error should name the offending field")
		})
	}
}

func TestValidateAcceptsSafeIdentifierVariants(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"home", "_home", "home-laptop", "Home2", "a"} {
		cfg := validConfig()
		cfg.Sets[0].Name = name
		assert.NoError(t, cfg.Validate(), "name %q should be valid", name)
	}
}
