package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// pull is the one way the engine pulls an image: a rollout, the re-pull of an
// image that was pruned, a job. It finds the credential for the image's
// registry — the one stored with `shipwick registry login`, and without one
// the runtime falls back to the Docker configuration file on the server — and
// turns a pull refused for authentication into a sentence that names the
// command to run.
func (e *Engine) pull(ctx context.Context, image string) error {
	registry, name, err := docker.ImageRegistry(image)
	if err != nil {
		return err
	}
	var auth *docker.RegistryAuth
	username, password, found, err := e.store.RegistryCredential(ctx, registry)
	if err != nil {
		return err
	}
	if found {
		auth = &docker.RegistryAuth{Registry: registry, Username: username, Password: password}
	}

	err = e.rt.PullImage(ctx, image, auth)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, docker.ErrPullDenied):
		return err
	case found:
		return fmt.Errorf("pull access denied for %s: the credential stored for %s does not give access to it; replace it with shipwick registry login %s (or check the image name)", name, registry, registry)
	}
	return fmt.Errorf("pull access denied for %s: run shipwick registry login %s (or check the image name)", name, registry)
}

// createContainer creates a container, and pulls its image first when the
// image has been pruned since it was deployed.
func (e *Engine) createContainer(ctx context.Context, cspec docker.ContainerSpec) (id, name string, err error) {
	id, name, err = e.rt.CreateContainer(ctx, cspec)
	if err == nil {
		return id, name, nil
	}
	if exists, ierr := e.rt.ImageExists(ctx, cspec.Image); ierr != nil || exists {
		return "", "", err
	}
	if spec.IsLocalImage(cspec.Image) {
		// Nowhere to pull it from: it came from a developer's machine.
		return "", "", fmt.Errorf("image %w", localImageMissing(cspec.Image))
	}
	if perr := e.pull(ctx, cspec.Image); perr != nil {
		return "", "", fmt.Errorf("image %s is gone and could not be pulled again: %w", cspec.Image, perr)
	}
	return e.rt.CreateContainer(ctx, cspec)
}

// RegistryLoginError means a credential was not stored because its registry
// did not accept it. Message is the registry's own, or why it could not be
// asked.
type RegistryLoginError struct {
	Registry string
	Refused  bool
	Message  string
}

func (e *RegistryLoginError) Error() string {
	if e.Refused {
		return fmt.Sprintf("%s refused the login: %s", e.Registry, e.Message)
	}
	return fmt.Sprintf("could not log in to %s: %s", e.Registry, e.Message)
}

// registryLoginTimeout bounds the check of a credential: a registry that does
// not answer is a typo in its name more often than a slow registry.
const registryLoginTimeout = 30 * time.Second

// RegistryLogin stores the credential for a registry once the registry has
// accepted it, so that a mistyped token is found here and not by the next
// deployment. It returns the name the credential is stored under.
func (e *Engine) RegistryLogin(ctx context.Context, registry, username, password string, now time.Time) (string, error) {
	registry, err := api.NormalizeRegistry(registry)
	if err != nil {
		return "", err
	}
	if err := api.ValidateRegistryCredential(username, password); err != nil {
		return "", err
	}

	loginCtx, cancel := context.WithTimeout(ctx, registryLoginTimeout)
	defer cancel()
	err = e.rt.RegistryLogin(loginCtx, docker.RegistryAuth{Registry: registry, Username: username, Password: password})
	var refused *docker.LoginError
	switch {
	case errors.As(err, &refused):
		return "", &RegistryLoginError{Registry: registry, Refused: refused.Refused, Message: refused.Message}
	case err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
		return "", &RegistryLoginError{Registry: registry, Message: fmt.Sprintf("no answer within %s", registryLoginTimeout)}
	case err != nil:
		return "", err
	}
	if err := e.store.SetRegistry(ctx, registry, username, password, now); err != nil {
		return "", err
	}
	return registry, nil
}
