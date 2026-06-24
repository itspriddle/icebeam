package logging

import (
	"log/slog"
	"slices"
	"strings"
)

// redactedPlaceholder is substituted for any value identified as a secret.
const redactedPlaceholder = "[REDACTED]"

// Secret wraps a sensitive string (a repository password, REST-server
// credential, etc.) so it can be threaded through the logging helpers without
// ever being emitted. It implements slog.LogValuer, rendering as the redacted
// placeholder regardless of the underlying value.
type Secret string

// LogValue satisfies slog.LogValuer, ensuring the wrapped secret never reaches
// the log output.
func (Secret) LogValue() slog.Value {
	return slog.StringValue(redactedPlaceholder)
}

// String also redacts so a Secret formatted with %v/%s outside slog is safe.
func (Secret) String() string { return redactedPlaceholder }

// sensitiveKeys are attribute keys whose values are redacted as a safety net,
// even when the caller passed a plain string rather than a Secret. Matching is
// case-insensitive and substring-based so "repo_password" or "RESTIC_PASSWORD"
// are caught.
var sensitiveKeys = []string{"password", "passwd", "secret", "token", "credential"}

// redactReplaceAttr is an slog ReplaceAttr hook. It resolves LogValuer values
// (so Secret renders as the placeholder) and redacts any attribute whose key
// looks sensitive, guarding against accidentally logging a raw credential.
func redactReplaceAttr(_ []string, a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()

	if isSensitiveKey(a.Key) && a.Value.Kind() != slog.KindGroup {
		a.Value = slog.StringValue(redactedPlaceholder)
	}

	return a
}

// isSensitiveKey reports whether an attribute key matches a known
// secret-bearing name (case-insensitive substring match).
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	return slices.ContainsFunc(sensitiveKeys, func(s string) bool {
		return strings.Contains(lower, s)
	})
}
