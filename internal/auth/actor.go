package auth

import (
	"context"
	"slices"
)

// Actor is the signed-in household member a request acts for. Handlers
// record Actor.Subject as updated_by on edits.
type Actor struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
}

// Actor returns the household member s belongs to.
func (s Session) Actor() Actor {
	return Actor{Subject: s.Subject, Email: s.Email, Name: s.Name, Groups: slices.Clone(s.Groups)}
}

type actorKey struct{}

// WithActor returns a copy of ctx carrying actor. The session middleware
// calls it for every authenticated request.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom returns the actor WithActor stored in ctx. ok is false when ctx
// has none, which behind the session middleware means a wiring bug.
func ActorFrom(ctx context.Context) (actor Actor, ok bool) {
	actor, ok = ctx.Value(actorKey{}).(Actor)
	return actor, ok
}
