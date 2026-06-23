// Package version exposes build-time version metadata for icebeam.
//
// The values are intended to be overridden at link time via -ldflags, e.g.
//
//	go build -ldflags "-X github.com/builtfast/icebeam/internal/version.Version=1.2.3"
//
// Until the release pipeline (US-012) wires real values, the defaults below are
// used for local and development builds.
package version

// Build-time metadata, overridable via -ldflags -X.
var (
	// Version is the semantic version of the build (e.g. "1.2.3").
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "none"
	// Date is the build timestamp.
	Date = "unknown"
)

// String renders the full version line, including commit and build date.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ")"
}
