package deploy

import (
	"context"
	"fmt"
	"slices"
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
// Only env values and the passwords of proxy.basic_auth are looked at: the
// values that are secrets. A placeholder anywhere else — an image tag, a
// command — is the CLI's to fill in, and is stored as it is.

// MissingSecret is one placeholder the server cannot fill in.
type MissingSecret struct {
	Variable string // the env variable whose value refers to it
	Name     string // the placeholder's name
	// Field is the deploy.yaml field that refers to it when that is not an
	// env variable: "proxy.basic_auth[0].password".
	Field string
}

// field is where deploy.yaml refers to the secret.
func (m MissingSecret) field() string {
	if m.Field != "" {
		return m.Field
	}
	return "env." + m.Variable
}

// MissingSecretsError is returned by Deploy when the document refers to
// secrets that are not stored on the server, all of them at once.
type MissingSecretsError struct {
	Missing []MissingSecret
}

func (e *MissingSecretsError) Error() string {
	names := make([]string, 0, len(e.Missing))
	where := "env"
	for _, m := range e.Missing {
		names = append(names, "${"+m.Name+"}")
		if m.Field != "" {
			where = "deploy.yaml"
		}
	}
	return where + " refers to " + strings.Join(names, ", ") + ", which " + pluralIs(len(names)) + " not stored on the server"
}

// Fields shapes the error like a validation error, one entry per reference
// that cannot be resolved: to the user it is one.
func (e *MissingSecretsError) Fields() []spec.FieldError {
	fields := make([]spec.FieldError, 0, len(e.Missing))
	for _, m := range e.Missing {
		fields = append(fields, spec.FieldError{
			Field:    m.field(),
			Message:  fmt.Sprintf("refers to ${%s}, which is not set where shipwick runs and not stored on the server", m.Name),
			Expected: "shipwick secret set " + m.Name,
		})
	}
	return fields
}

// UnusableSecretError is returned by Deploy when a stored secret was filled
// in where its value cannot be used: a basic-auth password that is too short.
// The document could not be judged before the value was known.
type UnusableSecretError struct {
	Problems []spec.FieldError
}

func (e *UnusableSecretError) Error() string {
	parts := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		parts = append(parts, p.Field+": "+p.Message)
	}
	return strings.Join(parts, "; ")
}

// Fields shapes the error like a validation error.
func (e *UnusableSecretError) Fields() []spec.FieldError { return e.Problems }

// resolveSecrets returns app with the placeholders in its env values and its
// basic-auth passwords filled in from the stored secrets. What the caller
// holds is left alone. $${NAME}, which the CLI leaves untouched in these
// values, becomes the literal ${NAME}.
func (e *Engine) resolveSecrets(ctx context.Context, app spec.App) (spec.App, error) {
	var accounts []spec.BasicAuth
	if app.Proxy != nil {
		accounts = app.Proxy.BasicAuth
	}
	values := make([]string, 0, len(app.Env)+len(accounts))
	for _, value := range app.Env {
		values = append(values, value)
	}
	for _, a := range accounts {
		values = append(values, a.Password)
	}

	var names []string
	seen := map[string]bool{}
	escaped := false
	for _, value := range values {
		for _, m := range spec.Placeholder.FindAllStringSubmatch(value, -1) {
			if m[1] != "" {
				escaped = true
			} else if !seen[m[2]] {
				seen[m[2]] = true
				names = append(names, m[2])
			}
		}
	}
	if len(names) == 0 && !escaped {
		return app, nil
	}

	secrets, err := e.store.GetSecrets(ctx, names)
	if err != nil {
		return spec.App{}, fmt.Errorf("read secrets: %w", err)
	}

	var missing []MissingSecret
	// fill resolves one value; at says where it stands, for the error.
	fill := func(value string, at MissingSecret) (filled string, referred []string) {
		filled = spec.Placeholder.ReplaceAllStringFunc(value, func(match string) string {
			m := spec.Placeholder.FindStringSubmatch(match)
			if m[1] != "" {
				return "${" + m[2] + "}"
			}
			referred = append(referred, m[2])
			secret, ok := secrets[m[2]]
			if !ok {
				at.Name = m[2]
				missing = append(missing, at)
				return match
			}
			return secret
		})
		return filled, referred
	}

	if len(app.Env) > 0 {
		env := make(map[string]string, len(app.Env))
		for variable, value := range app.Env {
			env[variable], _ = fill(value, MissingSecret{Variable: variable})
		}
		app.Env = env
	}

	var unusable []spec.FieldError
	if len(accounts) > 0 {
		proxy := *app.Proxy
		proxy.BasicAuth = slices.Clone(accounts)
		for i := range proxy.BasicAuth {
			field := fmt.Sprintf("proxy.basic_auth[%d].password", i)
			before := len(missing)
			password, referred := fill(proxy.BasicAuth[i].Password, MissingSecret{Field: field})
			proxy.BasicAuth[i].Password = password
			if len(referred) == 0 || len(missing) > before {
				continue // judged when the document was parsed, or reported as missing
			}
			// Parse let the placeholder pass; what it stood for is known now.
			if err := spec.ValidatePassword(password); err != nil {
				unusable = append(unusable, spec.FieldError{
					Field:    field,
					Message:  fmt.Sprintf("with ${%s} filled in from the server's secrets, the password %s", strings.Join(referred, "}, ${"), err),
					Expected: "shipwick secret set " + referred[0],
				})
			}
		}
		app.Proxy = &proxy
	}

	if len(missing) > 0 {
		// Deterministic order, for the error and for whoever reads it.
		sort.Slice(missing, func(i, j int) bool {
			if a, b := missing[i].field(), missing[j].field(); a != b {
				return a < b
			}
			return missing[i].Name < missing[j].Name
		})
		return spec.App{}, &MissingSecretsError{Missing: missing}
	}
	if len(unusable) > 0 {
		return spec.App{}, &UnusableSecretError{Problems: unusable}
	}
	return app, nil
}

func pluralIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
