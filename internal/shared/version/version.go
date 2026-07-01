// Package version exposes build metadata stamped in via -ldflags at build time.
package version

var (
	// Version is the semantic version of the build (e.g. "0.1.0").
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "none"
	// Date is the build timestamp (RFC3339).
	Date = "unknown"
)

// String returns a human-readable version line.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ")"
}
