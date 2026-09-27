package deploy

import (
	"context"

	"github.com/shipwick/shipwick/pkg/api"
)

type actorKey struct{}

// WithActor names who the operations made with ctx are performed by: the API
// token, as authenticated. Deployments record it; stop, start and delete name
// it in their events.
func WithActor(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, actorKey{}, name)
}

func actorFrom(ctx context.Context) string {
	name, _ := ctx.Value(actorKey{}).(string)
	return name
}

// byActor appends the actor to an event message, unless it is the root token:
// on a server with a single token, "stopped by root" would say nothing.
func byActor(ctx context.Context, message string) string {
	if actor := actorFrom(ctx); actor != "" && actor != api.RootTokenName {
		return message + " by " + actor
	}
	return message
}
