package api

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Limits on the registry credentials stored on the server. A password is
// whatever the registry issues: a token of a few dozen characters, or a JSON
// service-account key of a few kilobytes.
const (
	MaxRegistries             = 50
	MaxRegistryLength         = 255
	MaxRegistryUsernameLength = 255
	MaxRegistryPasswordBytes  = 16 * 1024
)

var registryHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// NormalizeRegistry turns what a user calls a registry into the name its
// credential is stored under, which is the one image references use: a
// hostname with an optional port, in lower case. Docker Hub goes by three
// names, and they are one registry.
func NormalizeRegistry(registry string) (string, error) {
	name := strings.ToLower(registry)
	invalid := fmt.Errorf("invalid registry %q: use its hostname, with a port if it has one, e.g. ghcr.io or registry.example.com:5000", registry)

	host, port, hasPort := strings.Cut(name, ":")
	if len(name) > MaxRegistryLength || !registryHostPattern.MatchString(host) {
		return "", invalid
	}
	if hasPort {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", invalid
		}
	}
	switch name {
	case "index.docker.io", "registry-1.docker.io":
		return "docker.io", nil
	}
	return name, nil
}

// ValidateRegistryCredential checks the body of PUT /registries/{registry}.
func ValidateRegistryCredential(username, password string) error {
	switch {
	case username == "":
		return fmt.Errorf("the username is empty")
	case len(username) > MaxRegistryUsernameLength:
		return fmt.Errorf("the username is too long (max %d characters)", MaxRegistryUsernameLength)
	case strings.ContainsFunc(username, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ':' }):
		return fmt.Errorf("the username must not contain a colon or control characters")
	case password == "":
		return fmt.Errorf("the password is empty")
	case len(password) > MaxRegistryPasswordBytes:
		return fmt.Errorf("the password is too large (%d KB, max %d KB)", len(password)/1024, MaxRegistryPasswordBytes/1024)
	case strings.ContainsRune(password, 0):
		return fmt.Errorf("the password must not contain NUL bytes")
	}
	return nil
}
