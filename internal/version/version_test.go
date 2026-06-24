package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringIncludesAllMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		commit  string
		date    string
		want    string
	}{
		{
			name:    "development defaults",
			version: "dev",
			commit:  "none",
			date:    "unknown",
			want:    "dev (commit none, built unknown)",
		},
		{
			name:    "release values",
			version: "1.2.3",
			commit:  "abc1234",
			date:    "2026-06-23T12:00:00Z",
			want:    "1.2.3 (commit abc1234, built 2026-06-23T12:00:00Z)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildString(tt.version, tt.commit, tt.date)
			assert.Equal(t, tt.want, got)
		})
	}
}

// buildString mirrors String using explicit inputs so tests stay independent of
// the package-level (ldflags-injected) variables.
func buildString(version, commit, date string) string {
	return version + " (commit " + commit + ", built " + date + ")"
}

func TestStringUsesPackageVariables(t *testing.T) {
	assert.Equal(t, buildString(Version, Commit, Date), String())
}
