package config

import (
	"fmt"
	"net/url"
	"strings"
)

// Backend scheme prefixes restic understands for a repository URL. A repository
// URL is either one of these "<scheme>:..." forms or a bare local filesystem
// path (no recognized scheme).
const (
	BackendREST   = "rest"
	BackendSFTP   = "sftp"
	BackendS3     = "s3"
	BackendB2     = "b2"
	BackendGS     = "gs"
	BackendAzure  = "azure"
	BackendRClone = "rclone"
	BackendSwift  = "swift"
	// BackendLocal is the synthetic scheme reported for a bare local path that
	// carries no recognized backend prefix.
	BackendLocal = "local"
)

// knownBackends is the set of restic backend schemes ParseRepoURL recognizes.
// Anything else with a "<scheme>:" prefix is rejected as unrecognized.
var knownBackends = map[string]struct{}{
	BackendREST:   {},
	BackendSFTP:   {},
	BackendS3:     {},
	BackendB2:     {},
	BackendGS:     {},
	BackendAzure:  {},
	BackendRClone: {},
	BackendSwift:  {},
}

// RepoURL is the result of parsing and normalizing a restic repository URL. It
// reports the backend scheme, exposes any HTTP credentials that were embedded in
// a rest: URL (stripped from URL so they never reach config), and carries the
// normalized URL with that userinfo removed.
type RepoURL struct {
	// Backend is the restic backend scheme (e.g. "rest", "sftp", "s3") or
	// "local" for a bare filesystem path.
	Backend string
	// URL is the repository URL with any embedded HTTP userinfo stripped. It is
	// safe to store in config.toml.
	URL string
	// RESTUsername and RESTPassword carry HTTP credentials that were embedded in
	// a rest:https://user:pass@host/... URL. They are empty when the URL carried
	// no userinfo. Secrets belong in the credential store, never the config.
	RESTUsername string
	RESTPassword string
	// hasRESTCredentials records whether userinfo was present, so an embedded
	// empty password is still distinguishable from "no credentials".
	hasRESTCredentials bool
}

// IsRESTEndpoint reports whether the repository is a restic REST server
// (rest:...) endpoint, for which icebeam prompts for and stores HTTP
// credentials.
func (r RepoURL) IsRESTEndpoint() bool {
	return r.Backend == BackendREST
}

// HasRESTCredentials reports whether the original URL embedded HTTP userinfo
// (user[:pass]@) that ParseRepoURL stripped into RESTUsername/RESTPassword. The
// caller surfaces this as a warning and routes the credentials to the credential
// store.
func (r RepoURL) HasRESTCredentials() bool {
	return r.hasRESTCredentials
}

// ParseRepoURL classifies and normalizes a restic repository URL. It is pure (no
// I/O). It returns the backend scheme, and for a rest:https://user:pass@host/...
// URL it strips the embedded HTTP userinfo into RESTUsername/RESTPassword and
// returns the URL without it (secrets must never be stored in config.toml).
//
// An empty URL, or one whose "<scheme>:" prefix is not a restic backend, is
// rejected with a field-named error matching the config.Validate style
// (repository.url: ...).
func ParseRepoURL(raw string) (RepoURL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return RepoURL{}, fmt.Errorf("repository.url: must not be empty")
	}

	scheme, rest, hasScheme := splitScheme(trimmed)

	// No "<scheme>:" prefix: treat as a bare local filesystem path.
	if !hasScheme {
		return RepoURL{Backend: BackendLocal, URL: trimmed}, nil
	}

	if _, ok := knownBackends[scheme]; !ok {
		return RepoURL{}, fmt.Errorf(
			"repository.url: %q is not a recognized restic backend (use rest, sftp, s3, b2, gs, azure, rclone, swift, or a local path)",
			scheme,
		)
	}

	if scheme != BackendREST {
		return RepoURL{Backend: scheme, URL: trimmed}, nil
	}

	return parseREST(trimmed, rest)
}

// splitScheme splits a "scheme:remainder" string at the first colon. It reports
// whether a syntactically valid scheme prefix was present. A leading colon, a
// Windows-style drive letter (a single-character scheme followed by a
// path-separator), or no colon at all are all treated as "no scheme" so a bare
// local path is not misclassified.
func splitScheme(s string) (scheme, remainder string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return "", s, false
	}

	scheme = s[:i]
	if !isSchemeName(scheme) {
		return "", s, false
	}

	return scheme, s[i+1:], true
}

// isSchemeName reports whether s looks like a backend scheme name: a leading
// letter followed by letters or digits (restic schemes like "s3", "b2", "gs"
// carry digits). Requiring a leading letter keeps a bare numeric or
// punctuation-laden local path segment from being read as a scheme.
func isSchemeName(s string) bool {
	for i, r := range s {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isDigit := r >= '0' && r <= '9'
		if i == 0 {
			if !isLetter {
				return false
			}
			continue
		}
		if !isLetter && !isDigit {
			return false
		}
	}
	return s != ""
}

// parseREST handles the rest: backend, whose remainder is itself an HTTP(S) URL
// that may embed user:pass@ HTTP credentials. The credentials are extracted and
// stripped so the stored URL carries none.
func parseREST(original, rest string) (RepoURL, error) {
	result := RepoURL{Backend: BackendREST, URL: original}

	// A bare "rest:" with no inner URL is unusable.
	if strings.TrimSpace(rest) == "" {
		return RepoURL{}, fmt.Errorf("repository.url: rest: repository requires a URL (e.g. rest:https://host:8000/path)")
	}

	inner, err := url.Parse(rest)
	if err != nil {
		return RepoURL{}, fmt.Errorf("repository.url: %q is not a valid rest: URL: %w", original, err)
	}

	// No embedded userinfo: the URL is already clean.
	if inner.User == nil {
		return result, nil
	}

	result.RESTUsername = inner.User.Username()
	if pw, ok := inner.User.Password(); ok {
		result.RESTPassword = pw
	}
	result.hasRESTCredentials = true

	// Strip the userinfo and rebuild the normalized URL.
	inner.User = nil
	result.URL = BackendREST + ":" + inner.String()

	return result, nil
}
