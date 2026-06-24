package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRepoURLClassifiesAndNormalizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		wantBackend  string
		wantURL      string
		wantREST     bool // IsRESTEndpoint
		wantHasCreds bool // HasRESTCredentials
		wantRESTUser string
		wantRESTPass string
	}{
		{
			name:        "rest without credentials",
			raw:         "rest:https://nas.local:8000/icebeam",
			wantBackend: BackendREST,
			wantURL:     "rest:https://nas.local:8000/icebeam",
			wantREST:    true,
		},
		{
			name:         "rest with embedded credentials",
			raw:          "rest:https://user:pass@nas.local:8000/icebeam",
			wantBackend:  BackendREST,
			wantURL:      "rest:https://nas.local:8000/icebeam",
			wantREST:     true,
			wantHasCreds: true,
			wantRESTUser: "user",
			wantRESTPass: "pass",
		},
		{
			name:         "rest with username but no password",
			raw:          "rest:https://user@nas.local:8000/icebeam",
			wantBackend:  BackendREST,
			wantURL:      "rest:https://nas.local:8000/icebeam",
			wantREST:     true,
			wantHasCreds: true,
			wantRESTUser: "user",
		},
		{
			name:         "rest with percent-encoded credentials",
			raw:          "rest:https://user:p%40ss@nas.local:8000/icebeam",
			wantBackend:  BackendREST,
			wantURL:      "rest:https://nas.local:8000/icebeam",
			wantREST:     true,
			wantHasCreds: true,
			wantRESTUser: "user",
			wantRESTPass: "p@ss",
		},
		{
			name:        "sftp",
			raw:         "sftp:user@host:/srv/restic-repo",
			wantBackend: BackendSFTP,
			wantURL:     "sftp:user@host:/srv/restic-repo",
		},
		{
			name:        "s3",
			raw:         "s3:s3.amazonaws.com/bucket_name",
			wantBackend: BackendS3,
			wantURL:     "s3:s3.amazonaws.com/bucket_name",
		},
		{
			name:        "b2",
			raw:         "b2:bucketname:path/to/repo",
			wantBackend: BackendB2,
			wantURL:     "b2:bucketname:path/to/repo",
		},
		{
			name:        "rclone",
			raw:         "rclone:remote:path/to/repo",
			wantBackend: BackendRClone,
			wantURL:     "rclone:remote:path/to/repo",
		},
		{
			name:        "bare local path",
			raw:         "/srv/restic-repo",
			wantBackend: BackendLocal,
			wantURL:     "/srv/restic-repo",
		},
		{
			name:        "relative local path",
			raw:         "backups/restic",
			wantBackend: BackendLocal,
			wantURL:     "backups/restic",
		},
		{
			name:        "trims surrounding whitespace",
			raw:         "  rest:https://nas.local:8000/icebeam  ",
			wantBackend: BackendREST,
			wantURL:     "rest:https://nas.local:8000/icebeam",
			wantREST:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseRepoURL(tt.raw)
			require.NoError(t, err)

			assert.Equal(t, tt.wantBackend, got.Backend, "backend scheme")
			assert.Equal(t, tt.wantURL, got.URL, "normalized URL")
			assert.Equal(t, tt.wantREST, got.IsRESTEndpoint(), "IsRESTEndpoint")
			assert.Equal(t, tt.wantHasCreds, got.HasRESTCredentials(), "HasRESTCredentials")
			assert.Equal(t, tt.wantRESTUser, got.RESTUsername, "REST username")
			assert.Equal(t, tt.wantRESTPass, got.RESTPassword, "REST password")
		})
	}
}

func TestParseRepoURLStripsCredentialsFromStoredURL(t *testing.T) {
	t.Parallel()

	got, err := ParseRepoURL("rest:https://user:s3cr3t@nas.local:8000/icebeam")
	require.NoError(t, err)

	assert.NotContains(t, got.URL, "s3cr3t", "password must never survive in the stored URL")
	assert.NotContains(t, got.URL, "user", "username must never survive in the stored URL")
	assert.Equal(t, "s3cr3t", got.RESTPassword)
	assert.Equal(t, "user", got.RESTUsername)
}

func TestParseRepoURLRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "whitespace only", raw: "   "},
		{name: "unknown scheme", raw: "ftp:host/path"},
		{name: "bare rest with no inner url", raw: "rest:"},
		{name: "rest with whitespace inner url", raw: "rest:   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseRepoURL(tt.raw)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "repository.url",
				"error must name the offending field")
		})
	}
}

func TestParseRepoURLDoesNotMistakeLocalPathForScheme(t *testing.T) {
	t.Parallel()

	// A path with a colon but no letters-only scheme must classify as local,
	// not trip the unknown-backend rejection.
	for _, raw := range []string{
		"./repo:dir",
		"/srv/repo:2024",
	} {
		got, err := ParseRepoURL(raw)
		require.NoError(t, err, "raw %q", raw)
		assert.Equal(t, BackendLocal, got.Backend, "raw %q", raw)
		assert.Equal(t, raw, got.URL)
	}
}
