package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// The configuration an application runs with, given back as the deploy.yaml
// that describes it.
//
// What the agent stores is the spec with its secret values filled in: the
// values the containers were started with. A value that the document wrote
// as a reference — postgres://app:${DB_PASSWORD}@db/app, filled in from the
// stored secrets — is remembered as that text next to the deployment
// (store.DeploymentReferences), and the document says the reference again. A
// value that arrived as a literal, typed into the file or filled in by the
// CLI from the environment, has no reference to go back to: the agent cannot
// tell a password from a log level, so it hands out neither. The document
// says spec.Mask in its place, and a mask is refused wherever a document
// arrives (resolveSecrets), so that it can never become a value.
//
// The text around a reference is given back as the document said it, which
// the API masks everywhere else. That is why the document is for those who
// may deploy the application: they can read its environment with a command
// run in it anyway.

// MaskedValuesError is returned for a document that holds the mask where a
// value belongs: one that was copied out of what the agent hands out.
type MaskedValuesError struct {
	Masked []string // the fields, as spec.MaskedFields names them
}

func (e *MaskedValuesError) Error() string {
	return strings.Join(e.Masked, ", ") + ": " + spec.Mask + " is what the server shows in the place of a value, not a value"
}

// Fields shapes the error like a validation error, one entry per mask.
func (e *MaskedValuesError) Fields() []spec.FieldError {
	fields := make([]spec.FieldError, 0, len(e.Masked))
	for _, field := range e.Masked {
		fields = append(fields, spec.FieldError{
			Field:    field,
			Message:  spec.Mask + " is what the server shows in the place of this value, not the value",
			Expected: "the value itself, or ${NAME} with the value stored by shipwick secret set NAME",
		})
	}
	return fields
}

// referencesOf is the references a deployment made from o is recorded with:
// its document's, or those of the deployment whose spec it re-uses. A redeploy
// and a rollback start the values they find, and the document they came from
// is still the one that wrote them.
func (e *Engine) referencesOf(ctx context.Context, o origin) (spec.References, error) {
	if !o.references.Empty() || o.sourceID == nil {
		return o.references, nil
	}
	return e.store.DeploymentReferences(ctx, *o.sourceID)
}

// Config returns the configuration of the application's active deployment as
// a document: see spec.Document for escape.
func (e *Engine) Config(ctx context.Context, name string, escape bool) (api.ApplicationConfig, error) {
	_, d, err := e.activeDeployment(ctx, name)
	if err != nil {
		return api.ApplicationConfig{}, err
	}
	references, err := e.store.DeploymentReferences(ctx, d.ID)
	if err != nil {
		return api.ApplicationConfig{}, err
	}
	document, masked, err := spec.Document(d.Spec, references, escape)
	if err != nil {
		return api.ApplicationConfig{}, fmt.Errorf("write the configuration of %s: %w", name, err)
	}
	if masked == nil {
		masked = []string{}
	}
	return api.ApplicationConfig{
		Application:  d.Application,
		DeploymentID: d.ID,
		Sequence:     d.Sequence,
		Version:      d.Version,
		Document:     document,
		Masked:       masked,
		StaticDigest: d.StaticDigest,
	}, nil
}

// exportedReferences is what an export says about the references of the
// deployment it carries: nothing when there are none.
func (e *Engine) exportedReferences(ctx context.Context, d store.Deployment) (*spec.References, error) {
	references, err := e.store.DeploymentReferences(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	if references = references.For(d.Spec); references.Empty() {
		return nil, nil
	}
	return &references, nil
}

// importedReferences checks the references an export carries for app. They
// arrive over the API and are given back in a document, so each is held to
// what a document could have said in that place: a value of the application,
// with a reference in it, of a document's size and without a NUL byte.
func importedReferences(app spec.App, carried *spec.References) (spec.References, error) {
	if carried == nil {
		return spec.References{}, nil
	}
	references := carried.For(app)
	if len(references.Env) != len(carried.Env) || len(references.BasicAuth) != len(carried.BasicAuth) {
		return spec.References{}, errors.New("its references name values the configuration does not have, or refer to nothing")
	}
	texts := make([]string, 0, len(references.Env)+len(references.BasicAuth))
	for _, text := range references.Env {
		texts = append(texts, text)
	}
	for _, text := range references.BasicAuth {
		texts = append(texts, text)
	}
	size := 0
	for _, text := range texts {
		if strings.ContainsRune(text, 0) {
			return spec.References{}, errors.New("its references hold a NUL byte")
		}
		size += len(text)
	}
	if size > spec.MaxConfigBytes {
		return spec.References{}, errors.New("its references are larger than a deploy.yaml may be")
	}
	return references, nil
}
