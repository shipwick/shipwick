package deploy

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/shipwick/shipwick/pkg/spec"
)

// A deploy.yaml may arrive with ${NAME} placeholders left in its env values:
// the CLI fills in what its own environment and --env-file know and leaves
// the rest to the server, where `shipwick secret set` has stored the values.
// The engine fills them in here, before the deployment is recorded, so the
// stored spec holds the values the containers were started with. Changing a
// secret therefore takes effect on the next deployment, not on running
// containers, and a redeploy or rollback re-uses stored values as they were.
//
// Only env values are looked at. A placeholder anywhere else — an image tag,
// a command — is the CLI's to fill in, and is stored as it is.

// MissingSecret is one placeholder the server cannot fill in.
type MissingSecret struct {
	Variable string // the env variable whose value refers to it
	Name     string // the placeholder's name
}

// MissingSecretsError is returned by Deploy when env values refer to secrets
// that are not stored on the server, all of them at once.
type MissingSecretsError struct {
	Missing []MissingSecret
}

func (e *MissingSecretsError) Error() string {
	names := make([]string, 0, len(e.Missing))
	for _, m := range e.Missing {
		names = append(names, "${"+m.Name+"}")
	}
	return "env refers to " + strings.Join(names, ", ") + ", which " + pluralIs(len(names)) + " not stored on the server"
}

// Fields shapes the error like a validation error, one entry per env
// variable that cannot be resolved: to the user it is one.
func (e *MissingSecretsError) Fields() []spec.FieldError {
	fields := make([]spec.FieldError, 0, len(e.Missing))
	for _, m := range e.Missing {
		fields = append(fields, spec.FieldError{
			Field:    "env." + m.Variable,
			Message:  fmt.Sprintf("refers to ${%s}, which is not set where shipwick runs and not stored on the server", m.Name),
			Expected: "shipwick secret set " + m.Name,
		})
	}
	return fields
}

// resolveSecrets returns app with the placeholders in its env values filled
// in from the stored secrets. The caller's map is left alone. $${NAME}, which
// the CLI leaves untouched in env values, becomes the literal ${NAME}.
func (e *Engine) resolveSecrets(ctx context.Context, app spec.App) (spec.App, error) {
	var names []string
	seen := map[string]bool{}
	for _, value := range app.Env {
		for _, m := range spec.Placeholder.FindAllStringSubmatch(value, -1) {
			if m[1] == "" && !seen[m[2]] {
				seen[m[2]] = true
				names = append(names, m[2])
			}
		}
	}
	if len(names) == 0 && !hasEscapedPlaceholder(app.Env) {
		return app, nil
	}

	secrets, err := e.store.GetSecrets(ctx, names)
	if err != nil {
		return spec.App{}, fmt.Errorf("read secrets: %w", err)
	}

	env := make(map[string]string, len(app.Env))
	var missing []MissingSecret
	for variable, value := range app.Env {
		env[variable] = spec.Placeholder.ReplaceAllStringFunc(value, func(match string) string {
			m := spec.Placeholder.FindStringSubmatch(match)
			if m[1] != "" {
				return "${" + m[2] + "}"
			}
			secret, ok := secrets[m[2]]
			if !ok {
				missing = append(missing, MissingSecret{Variable: variable, Name: m[2]})
				return match
			}
			return secret
		})
	}
	if len(missing) > 0 {
		// Deterministic order, for the error and for whoever reads it.
		sort.Slice(missing, func(i, j int) bool {
			if missing[i].Variable != missing[j].Variable {
				return missing[i].Variable < missing[j].Variable
			}
			return missing[i].Name < missing[j].Name
		})
		return spec.App{}, &MissingSecretsError{Missing: missing}
	}
	app.Env = env
	return app, nil
}

func hasEscapedPlaceholder(env map[string]string) bool {
	for _, value := range env {
		for _, m := range spec.Placeholder.FindAllStringSubmatch(value, -1) {
			if m[1] != "" {
				return true
			}
		}
	}
	return false
}

func pluralIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
