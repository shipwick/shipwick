package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

// RegistryAuth is a credential for one registry, named the way image
// references name it: "ghcr.io", "registry.example.com:5000", "docker.io".
type RegistryAuth struct {
	Registry string
	Username string
	Password string
}

func (a RegistryAuth) config() registry.AuthConfig {
	return registry.AuthConfig{Username: a.Username, Password: a.Password, ServerAddress: a.Registry}
}

// encode is the X-Registry-Auth value for a pull.
func (a RegistryAuth) encode() (string, error) {
	payload, err := json.Marshal(a.config())
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(payload), nil
}

// ImageRegistry returns the registry an image is pulled from, and the image's
// name without tag or digest as a user would write it: "ghcr.io" and
// "ghcr.io/company/api"; "docker.io" and "nginx".
func ImageRegistry(image string) (registry, name string, err error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return "", "", fmt.Errorf("parse image reference: %w", err)
	}
	return strings.ToLower(reference.Domain(named)), reference.FamiliarName(named), nil
}

// ErrPullDenied is wrapped by PullImage when the registry wanted a
// credential, or refused the one it was given.
var ErrPullDenied = errors.New("pull access denied")

// pullDenied recognises a pull refused for authentication. The daemon does
// not classify these consistently: depending on the registry and on the image
// store it runs, the same refusal arrives as 401, 403, 404 or 500, and only
// the text says what happened.
func pullDenied(err error) bool {
	if client.IsErrConnectionFailed(err) {
		return false
	}
	if cerrdefs.IsUnauthorized(err) || cerrdefs.IsPermissionDenied(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, sign := range []string{"pull access denied", "denied", "unauthorized", "authentication required", "access forbidden", "no basic auth credentials"} {
		if strings.Contains(msg, sign) {
			return true
		}
	}
	return false
}

// LoginError is returned by RegistryLogin when the registry did not accept
// the credential. Message is what the registry answered, or why it could not
// be asked; Refused tells the two apart.
type LoginError struct {
	Refused bool
	Message string
}

func (e *LoginError) Error() string { return e.Message }

// The daemon wraps the registry's answer in its own words and the request it
// made: `Error response from daemon: Get "https://ghcr.io/v2/": denied: denied`.
var loginNoise = regexp.MustCompile(`^(Error response from daemon: )?([A-Za-z]+ "[^"]*": )?`)

// RegistryLogin asks the daemon to check a credential against its registry.
// Nothing is kept by the daemon: the Engine API's login only verifies.
func (r *Runtime) RegistryLogin(ctx context.Context, auth RegistryAuth) error {
	_, err := r.cli.RegistryLogin(ctx, client.RegistryLoginOptions{
		Username:      auth.Username,
		Password:      auth.Password,
		ServerAddress: auth.Registry,
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || client.IsErrConnectionFailed(err) {
		return fmt.Errorf("check the credential for %s: %w", auth.Registry, err)
	}
	return loginError(err)
}

// loginError tells a registry that said no from one that could not be asked.
// As with pulls, the status the daemon answers with does not always say: a
// registry over plain HTTP that answers 401 arrives as a 500 whose text
// carries the 401.
func loginError(err error) *LoginError {
	msg := loginNoise.ReplaceAllString(err.Error(), "")
	refused := cerrdefs.IsUnauthorized(err) || cerrdefs.IsPermissionDenied(err)
	for _, sign := range []string{"401", "unauthorized", "denied", "incorrect username or password"} {
		refused = refused || strings.Contains(strings.ToLower(msg), sign)
	}
	return &LoginError{Refused: refused, Message: msg}
}
