// Package buildinfo holds the release version stamped into the panel and Agent
// binaries at build time.
package buildinfo

// Version is set with -ldflags "-X github.com/KurisuT7/portolan/internal/buildinfo.Version=v0.1.0".
// Development builds report "dev".
var Version = "dev"
