package docker

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/registry"
)

// dockerConfigPath locates the Docker CLI config file, honoring the standard
// DOCKER_CONFIG variable. Running `docker login` on the server is all it takes
// for Shipwick to pull private images.
func dockerConfigPath() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}

// registryAuth returns the encoded X-Registry-Auth value for the registry
// hosting image, or "" when no static credentials are configured. Credential
// helpers (credsStore) are not supported; such entries are skipped.
func registryAuth(configPath, image string) (string, error) {
	if configPath == "" {
		return "", nil
	}
	data, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("read docker config: %w", err)
	}

	var cfg struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse docker config %s: %w", configPath, err)
	}

	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", fmt.Errorf("parse image reference: %w", err)
	}
	host := reference.Domain(named)

	for key, entry := range cfg.Auths {
		if registryHost(key) != host || entry.Auth == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(entry.Auth)
		if err != nil {
			return "", fmt.Errorf("docker config: malformed auth entry for %s", host)
		}
		user, pass, ok := strings.Cut(string(decoded), ":")
		if !ok {
			return "", fmt.Errorf("docker config: malformed auth entry for %s", host)
		}
		payload, err := json.Marshal(registry.AuthConfig{
			Username:      user,
			Password:      pass,
			IdentityToken: entry.IdentityToken,
			ServerAddress: host,
		})
		if err != nil {
			return "", err
		}
		return base64.URLEncoding.EncodeToString(payload), nil
	}
	return "", nil
}

// registryHost normalizes a docker config key ("ghcr.io", "https://ghcr.io/",
// "https://index.docker.io/v1/") to the registry domain used in image references.
func registryHost(key string) string {
	host := key
	if strings.Contains(key, "://") {
		if u, err := url.Parse(key); err == nil {
			host = u.Host
		}
	} else if i := strings.IndexByte(key, '/'); i >= 0 {
		host = key[:i]
	}
	switch host {
	case "index.docker.io", "registry-1.docker.io":
		return "docker.io"
	}
	return host
}
