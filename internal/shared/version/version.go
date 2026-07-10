// Package version exposes build metadata stamped in via -ldflags at build time.
package version

var (
	// Version is the semantic version of the build. release-please rewrites
	// this line on every tagged release — do not edit manually; land
	// Conventional Commits (feat/fix/…) on main and let the release-please PR
	// bump it. Overrideable at build time via `-ldflags "-X …Version=…"`.
	Version = "0.10.0" // x-release-please-version
	// Commit is the git commit the binary was built from.
	Commit = "none"
	// Date is the build timestamp (RFC3339).
	Date = "unknown"
)

// String returns a human-readable version line.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ")"
}
