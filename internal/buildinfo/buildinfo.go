// Package buildinfo holds the release versions stamped into the panel and Agent
// binaries at build time.
package buildinfo

// Version is set with -ldflags "-X github.com/KurisuT7/Portolan/internal/buildinfo.Version=v0.1.0".
// Development builds report "dev". The Agent binary is stamped with AgentVersion.
var Version = "dev"

// AgentVersion is the release in which the Agent the panel serves last changed.
// It can be older than Version when a release only changes the panel.
var AgentVersion = "dev"
