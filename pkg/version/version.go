// Package version holds the build version shared by the agent and the CLI.
package version

// Version is overridden at build time with:
//
//	-ldflags "-X github.com/shipwick/shipwick/pkg/version.Version=v1.2.3"
var Version = "dev"
