// Package version holds the binary version, injectable via ldflags at build time.
package version

// Version defaults to "dev" for local builds; goreleaser and make local-release
// override it via -ldflags "-X github.com/riftwerx/company-research/internal/version.Version=<tag>".
var Version = "dev"
